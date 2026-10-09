package mcpserver

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/McGeaverBeaver/opendvr/internal/dvr"
)

// addDVRTools registers the recording tools: the DVR is the part of the
// server an assistant is most useful for, so these are deliberately
// complete: search the guide, record once or as a pass, list and manage.
func addDVRTools(srv *mcp.Server, d Deps) {
	if d.DVR == nil {
		return
	}
	ro := &mcp.ToolAnnotations{ReadOnlyHint: true}
	type empty struct{}

	toolCount++
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "dvr_status",
		Description: "DVR overview: recordings folder and free disk space, whether commercial detection (comskip) is available, tuner count, number of passes and recordings by state (scheduled, conflict, recording, processing, ready, failed).",
		Annotations: ro,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ empty) (*mcp.CallToolResult, any, error) {
		return text(d.DVR.Summary())
	})

	type searchIn struct {
		Query string `json:"query" jsonschema:"Words to find in programme titles, episode titles and synopses, e.g. 'jeopardy' or 'late night'"`
		Limit int    `json:"limit,omitempty" jsonschema:"Most airings to return (default 50)"`
	}
	toolCount++
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "search_guide",
		Description: "Find upcoming airings in the guide by words in the title, episode title or synopsis, soonest first. Each hit gives the channel and the programme's start time (unix seconds), which record_program and create_pass take.",
		Annotations: ro,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in searchIn) (*mcp.CallToolResult, any, error) {
		if strings.TrimSpace(in.Query) == "" {
			return nil, nil, fmt.Errorf("query is required")
		}
		n := in.Limit
		if n <= 0 {
			n = 50
		}
		hits := d.DVR.Search(ctx, in.Query, n)
		type hit struct {
			Channel      string `json:"channel"`
			ChannelName  string `json:"channelName"`
			Title        string `json:"title"`
			EpisodeTitle string `json:"episodeTitle,omitempty"`
			Episode      string `json:"episodeNumber,omitempty"`
			SeriesID     string `json:"seriesId,omitempty"`
			Airing       int64  `json:"airing"`
			Starts       string `json:"starts"`
			Ends         string `json:"ends"`
			New          bool   `json:"new,omitempty"`
			Synopsis     string `json:"synopsis,omitempty"`
		}
		out := make([]hit, 0, len(hits))
		for _, h := range hits {
			p := h.Program
			out = append(out, hit{
				Channel: h.Channel, ChannelName: h.ChannelName, Title: p.Title, EpisodeTitle: p.EpisodeTitle, Episode: p.EpisodeNumber,
				SeriesID: p.SeriesID, Airing: p.Start, Starts: time.Unix(p.Start, 0).Format(time.RFC1123), Ends: time.Unix(p.End, 0).Format(time.RFC1123),
				New: p.New, Synopsis: p.Synopsis,
			})
		}
		return text(out)
	})

	type recordIn struct {
		Channel     string `json:"channel" jsonschema:"Guide number such as 5.1"`
		Airing      int64  `json:"airing" jsonschema:"The programme's start time in unix seconds, from search_guide or get_guide"`
		PadBefore   *int   `json:"padBefore,omitempty" jsonschema:"Minutes to start early (default 1)"`
		PadAfter    *int   `json:"padAfter,omitempty" jsonschema:"Minutes to run late (default 3)"`
		Commercials string `json:"commercials,omitempty" jsonschema:"skip (mark adverts so players jump them), remove (cut them out of the file) or none"`
	}
	toolCount++
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "record_program",
		Description: "Record one airing from the guide. If it is on now it starts within seconds. Returns the scheduled recording; status 'conflict' means every tuner will be busy at that time.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in recordIn) (*mcp.CallToolResult, any, error) {
		if in.Channel == "" || in.Airing == 0 {
			return nil, nil, fmt.Errorf("channel and airing are required")
		}
		rec, err := d.DVR.RecordAiring(in.Channel, in.Airing, dvr.Options{PadBefore: in.PadBefore, PadAfter: in.PadAfter, Commercials: dvr.Commercials(in.Commercials)})
		if err != nil {
			return nil, nil, err
		}
		d.DVR.RequestPlan()
		return text(rec)
	})

	type nowIn struct {
		Channel     string `json:"channel" jsonschema:"Guide number such as 5.1"`
		Minutes     int    `json:"minutes,omitempty" jsonschema:"How long to record; default is until the current programme ends (60 minutes when the guide has nothing)"`
		Commercials string `json:"commercials,omitempty" jsonschema:"skip, remove or none"`
	}
	toolCount++
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "record_now",
		Description: "Start recording a channel immediately, for the rest of the current programme or a set number of minutes.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in nowIn) (*mcp.CallToolResult, any, error) {
		if in.Channel == "" {
			return nil, nil, fmt.Errorf("channel is required")
		}
		rec, err := d.DVR.RecordNow(in.Channel, in.Minutes, dvr.Options{Commercials: dvr.Commercials(in.Commercials)})
		if err != nil {
			return nil, nil, err
		}
		d.DVR.RequestPlan()
		return text(rec)
	})

	type passIn struct {
		Title       string `json:"title" jsonschema:"Programme title to record, matched exactly (case-insensitive)"`
		SeriesID    string `json:"seriesId,omitempty" jsonschema:"Guide series id to match instead of the title, from search_guide"`
		Channel     string `json:"channel,omitempty" jsonschema:"Only this channel; empty means any channel"`
		NewOnly     bool   `json:"newOnly,omitempty" jsonschema:"Only first airings (skip repeats)"`
		Season      int    `json:"season,omitempty" jsonschema:"Only this season number"`
		Keep        int    `json:"keep,omitempty" jsonschema:"Keep only the newest N recordings; 0 keeps all"`
		PadBefore   *int   `json:"padBefore,omitempty" jsonschema:"Minutes to start early (default 1)"`
		PadAfter    *int   `json:"padAfter,omitempty" jsonschema:"Minutes to run late (default 3)"`
		Commercials string `json:"commercials,omitempty" jsonschema:"skip (default when comskip is available), remove or none"`
		Priority    int    `json:"priority,omitempty" jsonschema:"Lower wins when tuners are short; default is after existing passes"`
	}
	toolCount++
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "create_pass",
		Description: "Create a series pass: every future airing of a programme is recorded automatically, with options for new episodes only, one season, how many to keep, padding and commercial handling. Returns the pass; the schedule updates within seconds.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in passIn) (*mcp.CallToolResult, any, error) {
		r := dvr.Rule{Title: in.Title, SeriesID: in.SeriesID, Channel: in.Channel, NewOnly: in.NewOnly, Season: in.Season, Keep: in.Keep, Commercials: dvr.Commercials(in.Commercials), Priority: in.Priority}
		if in.PadBefore != nil {
			r.PadBefore = *in.PadBefore
		}
		if in.PadAfter != nil {
			r.PadAfter = *in.PadAfter
		}
		out, err := d.DVR.SaveRule(r)
		if err != nil {
			return nil, nil, err
		}
		return text(out)
	})

	type slotIn struct {
		Channel     string `json:"channel" jsonschema:"Guide number such as 5.1"`
		TimeOfDay   string `json:"timeOfDay" jsonschema:"Start time HH:MM in the server's local time"`
		DurationMin int    `json:"durationMin" jsonschema:"Length in minutes"`
		Days        []int  `json:"days,omitempty" jsonschema:"Weekdays to repeat on, 0 = Sunday .. 6 = Saturday"`
		Date        string `json:"date,omitempty" jsonschema:"A single date YYYY-MM-DD instead of weekdays"`
		Title       string `json:"title,omitempty" jsonschema:"Name for the recordings; defaults to channel and time"`
		Keep        int    `json:"keep,omitempty" jsonschema:"Keep only the newest N; 0 keeps all"`
		Commercials string `json:"commercials,omitempty" jsonschema:"skip, remove or none"`
	}
	toolCount++
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "create_timeslot",
		Description: "Record a channel at a fixed time of day, on chosen weekdays or on one date, regardless of what the guide says is on. For programmes the guide does not list, or to record a whole block.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in slotIn) (*mcp.CallToolResult, any, error) {
		r := dvr.Rule{Kind: dvr.RuleKindManual, Channel: in.Channel, TimeOfDay: in.TimeOfDay, DurationMin: in.DurationMin, Days: in.Days, Date: in.Date, Title: in.Title, Keep: in.Keep, Commercials: dvr.Commercials(in.Commercials)}
		out, err := d.DVR.SaveRule(r)
		if err != nil {
			return nil, nil, err
		}
		return text(out)
	})

	toolCount++
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "comskip_settings",
		Description: "Commercial detection settings: the default comskip .ini and any per-channel overrides (a channel file with opendvr_disable=1 turns detection off there), plus the recordings whose original broadcast file is still kept and can be tested on.",
		Annotations: ro,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ empty) (*mcp.CallToolResult, any, error) {
		type kept struct {
			ID, Title, Channel, Commercials string
			Airing                          string
			Cut                             bool
			Breaks                          int
			Minutes                         float64
		}
		var ks []kept
		for _, r := range d.DVR.KeptOriginals() {
			ks = append(ks, kept{ID: r.ID, Title: r.Title, Channel: r.Channel + " " + r.ChannelName, Commercials: string(r.Commercials),
				Airing: time.Unix(r.Airing, 0).Format(time.RFC1123), Cut: r.Cut, Breaks: len(r.Skips), Minutes: r.DurationSec / 60})
		}
		return text(map[string]any{"settings": d.DVR.ComskipSettings(), "testable": ks})
	})

	type testIn struct {
		RecordingID string `json:"recordingId" jsonschema:"A recording whose original broadcast file is kept (see comskip_settings)"`
		Settings    string `json:"settings,omitempty" jsonschema:"Complete comskip .ini text to try; empty uses what the recording's channel uses now"`
	}
	toolCount++
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "test_comskip",
		Description: "Run commercial detection on a kept recording with the given settings, changing nothing. Starts in the background and returns a test id; scans take a few minutes. Read the outcome with comskip_test_result.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in testIn) (*mcp.CallToolResult, any, error) {
		t, err := d.DVR.TestComskip(in.RecordingID, in.Settings)
		if err != nil {
			return nil, nil, err
		}
		return text(t)
	})

	type idOnly struct {
		ID string `json:"id" jsonschema:"The test id"`
	}
	toolCount++
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "comskip_test_result",
		Description: "The outcome of a test_comskip run: done or not, the commercial breaks found (seconds from the start), total commercial time and its share of the recording (network TV runs about 25-30%, public TV about 0), and comskip's own output.",
		Annotations: ro,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in idOnly) (*mcp.CallToolResult, any, error) {
		t, ok := d.DVR.ComskipTestResult(in.ID)
		if !ok {
			return nil, nil, fmt.Errorf("no such test")
		}
		return text(t)
	})

	type saveIn struct {
		Channel  string `json:"channel,omitempty" jsonschema:"Guide number to set an override for; empty sets the default"`
		Settings string `json:"settings" jsonschema:"Complete comskip .ini text. For a channel, empty removes its override; opendvr_disable=1 turns detection off there"`
	}
	toolCount++
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "save_comskip_settings",
		Description: "Save commercial detection settings, as the default or for one channel. New recordings use them; redetect_commercials applies them to kept recordings.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in saveIn) (*mcp.CallToolResult, any, error) {
		if err := d.DVR.SaveComskipSettings(in.Channel, in.Settings); err != nil {
			return nil, nil, err
		}
		return text(map[string]any{"saved": true, "channel": in.Channel})
	})

	type redetectIn struct {
		ID          string `json:"id" jsonschema:"The recording id"`
		Commercials string `json:"commercials,omitempty" jsonschema:"Optionally change the handling too: skip, remove or none"`
	}
	toolCount++
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "redetect_commercials",
		Description: "Process a recording again from its kept broadcast file with the current settings: find commercials and rebuild the MP4 (cutting them if the recording is set to remove).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in redetectIn) (*mcp.CallToolResult, any, error) {
		r, err := d.DVR.Redetect(in.ID, dvr.Commercials(in.Commercials))
		if err != nil {
			return nil, nil, err
		}
		return text(r)
	})

	toolCount++
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_passes",
		Description: "List the series passes in priority order with their options.",
		Annotations: ro,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ empty) (*mcp.CallToolResult, any, error) {
		return text(d.DVR.Rules())
	})

	type idIn struct {
		ID string `json:"id" jsonschema:"The pass or recording id"`
	}
	toolCount++
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "delete_pass",
		Description: "Delete a series pass and un-schedule what it had planned. Recordings already made are kept.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in idIn) (*mcp.CallToolResult, any, error) {
		if err := d.DVR.DeleteRule(in.ID); err != nil {
			return nil, nil, err
		}
		return text(map[string]any{"deleted": in.ID})
	})

	type listIn struct {
		Status string `json:"status,omitempty" jsonschema:"upcoming (scheduled, conflict, recording), library (recording, processing, ready, failed), or one state; default all"`
	}
	toolCount++
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_recordings",
		Description: "List recordings: upcoming ones with their times and conflicts, or the library with file sizes, durations, detected commercial spans and any error.",
		Annotations: ro,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listIn) (*mcp.CallToolResult, any, error) {
		recs := d.DVR.Recordings(in.Status)
		type view struct {
			dvr.Recording
			Starts string `json:"starts"`
			Ends   string `json:"ends"`
		}
		out := make([]view, 0, len(recs))
		for _, r := range recs {
			out = append(out, view{Recording: r, Starts: time.Unix(r.Start, 0).Format(time.RFC1123), Ends: time.Unix(r.End, 0).Format(time.RFC1123)})
		}
		return text(out)
	})

	toolCount++
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "delete_recording",
		Description: "Cancel a scheduled or running recording, or delete a finished one and its file. This cannot be undone.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in idIn) (*mcp.CallToolResult, any, error) {
		if err := d.DVR.Delete(in.ID); err != nil {
			return nil, nil, err
		}
		return text(map[string]any{"deleted": in.ID})
	})
}
