// Package apksign signs the TV app with a key that belongs to this server.
//
// CI builds the APK on a fresh machine every time, so each build carries a
// different throwaway debug signature, and Android refuses to install an
// update whose signature differs from the app already installed. The
// server therefore re-signs the APK it ships with its own key, created on
// first use and kept in the data directory. Every build a server hands out
// then carries the same signature and updates install over the previous
// version. The key never leaves the server; nothing secret lives in the
// repository or in CI.
//
// Only APK Signature Scheme v2 is written. Every Android version the app
// supports (7.0 and later) verifies it. An existing APK Signing Block is
// replaced; ZIP entries are not touched, so their alignment is kept. An APK
// that also carries a v1 (JAR) signature is refused rather than re-signed,
// since its stale JAR signature would name the old key.
package apksign

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	blockMagic = "APK Sig Block 42"
	// v2BlockID is the APK Signature Scheme v2 block's ID in the signing block.
	v2BlockID = 0x7109871a
	// algRSAPKCS1SHA256 is RSASSA-PKCS1-v1_5 with SHA2-256 over chunked
	// SHA2-256 content digests; deterministic, so re-signing the same build
	// gives the same bytes.
	algRSAPKCS1SHA256 = 0x0103
	chunkSize         = 1 << 20
	eocdSize          = 22
)

// Signer holds the key and its self-signed certificate.
type Signer struct {
	key  *rsa.PrivateKey
	cert []byte // DER
	spki []byte // SubjectPublicKeyInfo DER
}

// LoadOrCreate reads the key and certificate at path, or creates a new
// 2048-bit RSA key with a self-signed certificate there (mode 0600). The
// boolean reports whether a new key was made.
func LoadOrCreate(path string) (*Signer, bool, error) {
	b, err := os.ReadFile(path)
	if err == nil {
		s, err := parse(b)
		if err != nil {
			return nil, false, fmt.Errorf("%s: %w", path, err)
		}
		return s, false, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, false, err
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, false, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 62))
	if err != nil {
		return nil, false, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:       serial,
		Subject:            pkix.Name{CommonName: "OpenDVR TV app", Organization: []string{"OpenDVR"}},
		NotBefore:          time.Now().Add(-24 * time.Hour),
		NotAfter:           time.Now().AddDate(100, 0, 0), // Android does not check APK certificate dates
		SignatureAlgorithm: x509.SHA256WithRSA,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, false, err
	}
	pk8, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, false, err
	}
	var buf bytes.Buffer
	_ = pem.Encode(&buf, &pem.Block{Type: "PRIVATE KEY", Bytes: pk8})
	_ = pem.Encode(&buf, &pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, false, err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o600); err != nil {
		return nil, false, err
	}
	if err := os.Rename(tmp, path); err != nil {
		return nil, false, err
	}
	s, err := parse(buf.Bytes())
	return s, true, err
}

func parse(b []byte) (*Signer, error) {
	var key *rsa.PrivateKey
	var cert []byte
	for {
		var blk *pem.Block
		blk, b = pem.Decode(b)
		if blk == nil {
			break
		}
		switch blk.Type {
		case "PRIVATE KEY":
			k, err := x509.ParsePKCS8PrivateKey(blk.Bytes)
			if err != nil {
				return nil, err
			}
			rk, ok := k.(*rsa.PrivateKey)
			if !ok {
				return nil, errors.New("signing key is not RSA")
			}
			key = rk
		case "CERTIFICATE":
			cert = blk.Bytes
		}
	}
	if key == nil || cert == nil {
		return nil, errors.New("file must hold a PRIVATE KEY and a CERTIFICATE")
	}
	c, err := x509.ParseCertificate(cert)
	if err != nil {
		return nil, err
	}
	pub, ok := c.PublicKey.(*rsa.PublicKey)
	if !ok || !pub.Equal(&key.PublicKey) {
		return nil, errors.New("certificate does not match the key")
	}
	spki, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return nil, err
	}
	return &Signer{key: key, cert: cert, spki: spki}, nil
}

// Fingerprint is the certificate's SHA-256, as Android tools print it.
func (s *Signer) Fingerprint() string {
	sum := sha256.Sum256(s.cert)
	parts := make([]string, len(sum))
	for i, b := range sum {
		parts[i] = fmt.Sprintf("%02X", b)
	}
	return strings.Join(parts, ":")
}

// zipLayout locates the parts of an APK that v2 signing works with.
type zipLayout struct {
	entriesEnd int // where the ZIP entries end: the signing block or the central directory starts here
	cdStart    int
	eocdStart  int
}

func layout(apk []byte) (zipLayout, error) {
	var l zipLayout
	if len(apk) < eocdSize {
		return l, errors.New("not a ZIP file")
	}
	// The end record is the last 22 bytes plus a comment of up to 64 KiB.
	l.eocdStart = -1
	for i := len(apk) - eocdSize; i >= 0 && i >= len(apk)-eocdSize-0xffff; i-- {
		if binary.LittleEndian.Uint32(apk[i:]) == 0x06054b50 &&
			i+eocdSize+int(binary.LittleEndian.Uint16(apk[i+20:])) == len(apk) {
			l.eocdStart = i
			break
		}
	}
	if l.eocdStart < 0 {
		return l, errors.New("no ZIP end of central directory record")
	}
	cdSize := binary.LittleEndian.Uint32(apk[l.eocdStart+12:])
	cdOff := binary.LittleEndian.Uint32(apk[l.eocdStart+16:])
	if cdSize == 0xffffffff || cdOff == 0xffffffff {
		return l, errors.New("ZIP64 APKs are not supported")
	}
	l.cdStart = int(cdOff)
	if l.cdStart+int(cdSize) != l.eocdStart {
		return l, errors.New("central directory is not followed by the end record")
	}
	l.entriesEnd = l.cdStart
	// An existing APK Signing Block sits right before the central directory:
	// uint64 size, pairs, uint64 size, magic.
	if l.cdStart >= 32 && string(apk[l.cdStart-16:l.cdStart]) == blockMagic {
		size := binary.LittleEndian.Uint64(apk[l.cdStart-24:])
		if size < 24 || size > uint64(l.cdStart-8) {
			return l, errors.New("corrupt APK Signing Block")
		}
		start := l.cdStart - int(size) - 8
		if binary.LittleEndian.Uint64(apk[start:]) != size {
			return l, errors.New("corrupt APK Signing Block")
		}
		l.entriesEnd = start
	}
	return l, nil
}

// HasJARSignature reports whether the APK carries v1 signature files.
func HasJARSignature(apk []byte) bool {
	l, err := layout(apk)
	if err != nil {
		return false
	}
	cd := apk[l.cdStart:l.eocdStart]
	for p := 0; p+46 <= len(cd) && binary.LittleEndian.Uint32(cd[p:]) == 0x02014b50; {
		nameLen := int(binary.LittleEndian.Uint16(cd[p+28:]))
		extraLen := int(binary.LittleEndian.Uint16(cd[p+30:]))
		commentLen := int(binary.LittleEndian.Uint16(cd[p+32:]))
		if p+46+nameLen > len(cd) {
			break
		}
		name := strings.ToUpper(string(cd[p+46 : p+46+nameLen]))
		if strings.HasPrefix(name, "META-INF/") && (strings.HasSuffix(name, ".SF") || strings.HasSuffix(name, ".RSA") ||
			strings.HasSuffix(name, ".DSA") || strings.HasSuffix(name, ".EC")) {
			return true
		}
		p += 46 + nameLen + extraLen + commentLen
	}
	return false
}

// Sign returns the APK with its signing block replaced by a v2 signature
// from s.
func (s *Signer) Sign(apk []byte) ([]byte, error) {
	l, err := layout(apk)
	if err != nil {
		return nil, err
	}
	if HasJARSignature(apk) {
		return nil, errors.New("the APK carries a v1 (JAR) signature; build it with v1 signing off")
	}
	entries := apk[:l.entriesEnd]
	cd := apk[l.cdStart:l.eocdStart]
	eocd := append([]byte(nil), apk[l.eocdStart:]...)
	// The digest sees the end record pointing at the signing block, which is
	// where the central directory starts in the unsigned layout.
	binary.LittleEndian.PutUint32(eocd[16:], uint32(l.entriesEnd))
	block, err := s.signingBlock(contentDigest(entries, cd, eocd))
	if err != nil {
		return nil, err
	}
	newCD := l.entriesEnd + len(block)
	if newCD > 0xfffffffe {
		return nil, errors.New("APK too large")
	}
	binary.LittleEndian.PutUint32(eocd[16:], uint32(newCD))
	out := make([]byte, 0, len(entries)+len(block)+len(cd)+len(eocd))
	out = append(out, entries...)
	out = append(out, block...)
	out = append(out, cd...)
	out = append(out, eocd...)
	return out, nil
}

// contentDigest is v2's chunked SHA-256: each section in 1 MiB chunks,
// each chunk hashed as 0xa5 || uint32 length || data, then the chunk
// digests hashed as 0x5a || uint32 count || digests.
func contentDigest(sections ...[]byte) []byte {
	var digests []byte
	var n uint32
	var lenBuf [4]byte
	for _, sec := range sections {
		for off := 0; off < len(sec); off += chunkSize {
			end := min(off+chunkSize, len(sec))
			h := sha256.New()
			h.Write([]byte{0xa5})
			binary.LittleEndian.PutUint32(lenBuf[:], uint32(end-off))
			h.Write(lenBuf[:])
			h.Write(sec[off:end])
			digests = h.Sum(digests)
			n++
		}
	}
	h := sha256.New()
	h.Write([]byte{0x5a})
	binary.LittleEndian.PutUint32(lenBuf[:], n)
	h.Write(lenBuf[:])
	h.Write(digests)
	return h.Sum(nil)
}

func u32(v uint32) []byte { return binary.LittleEndian.AppendUint32(nil, v) }

// seq encodes elements as a sequence of uint32-length-prefixed values.
func seq(elems ...[]byte) []byte {
	var out []byte
	for _, e := range elems {
		out = binary.LittleEndian.AppendUint32(out, uint32(len(e)))
		out = append(out, e...)
	}
	return out
}

// signingBlock builds the APK Signing Block holding one v2 signer.
func (s *Signer) signingBlock(digest []byte) ([]byte, error) {
	signedData := seq(
		seq(append(u32(algRSAPKCS1SHA256), seq(digest)...)), // digests
		seq(s.cert), // certificates
		nil,         // additional attributes: none
	)
	sum := sha256.Sum256(signedData)
	sig, err := rsa.SignPKCS1v15(nil, s.key, crypto.SHA256, sum[:])
	if err != nil {
		return nil, err
	}
	signer := seq(
		signedData,
		seq(append(u32(algRSAPKCS1SHA256), seq(sig)...)), // signatures
		s.spki,
	)
	v2 := seq(seq(signer)) // sequence of signers
	pair := binary.LittleEndian.AppendUint64(nil, uint64(4+len(v2)))
	pair = append(pair, u32(v2BlockID)...)
	pair = append(pair, v2...)
	size := uint64(len(pair) + 8 + len(blockMagic))
	block := binary.LittleEndian.AppendUint64(nil, size)
	block = append(block, pair...)
	block = binary.LittleEndian.AppendUint64(block, size)
	block = append(block, blockMagic...)
	return block, nil
}
