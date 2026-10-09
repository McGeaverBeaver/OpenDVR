// Command fakeidp runs the test identity provider on a fixed port for
// manual testing of the login flow. Not part of the shipped binary.
package main

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"

	"github.com/McGeaverBeaver/opendvr/internal/auth/authtest"
)

func main() {
	idp := authtest.New("cc")
	// Re-bind to a fixed address so the issuer URL is stable across runs.
	idp.Server.Close()
	l, err := net.Listen("tcp", "0.0.0.0:9400")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	// A fresh http.Server: the closed one refuses new connections.
	idp.Server = &httptest.Server{Listener: l, Config: &http.Server{Handler: idp.Server.Config.Handler}}
	idp.Server.Start()
	if len(os.Args) > 1 {
		// The issuer the app and the browser both use to reach this process.
		idp.URL = os.Args[1]
	}
	fmt.Println("fake IdP at", idp.URL)
	select {}
}
