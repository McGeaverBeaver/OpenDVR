package apksign

import (
	"archive/zip"
	"bytes"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// testAPK is a ZIP with entries big enough to span several 1 MiB chunks.
func testAPK(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for i, size := range []int{100, 3<<20 + 7, 5000} {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: string(rune('a'+i)) + ".bin", Method: zip.Store})
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write(bytes.Repeat([]byte{byte(i + 1)}, size))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// verify is an independent reading of the v2 format: it finds the block,
// recomputes the content digest and checks the signature and the key.
func verify(t *testing.T, apk []byte) []byte {
	t.Helper()
	l, err := layout(apk)
	if err != nil {
		t.Fatal(err)
	}
	if l.entriesEnd == l.cdStart {
		t.Fatal("no signing block")
	}
	block := apk[l.entriesEnd:l.cdStart]
	size := binary.LittleEndian.Uint64(block)
	if int(size)+8 != len(block) || string(block[len(block)-16:]) != blockMagic {
		t.Fatalf("block framing: size %d, len %d", size, len(block))
	}
	pairs := block[8 : len(block)-24]
	var v2 []byte
	for len(pairs) > 0 {
		n := binary.LittleEndian.Uint64(pairs)
		id := binary.LittleEndian.Uint32(pairs[8:])
		if id == v2BlockID {
			v2 = pairs[12 : 8+n]
		}
		pairs = pairs[8+n:]
	}
	if v2 == nil {
		t.Fatal("no v2 block")
	}
	read := func(b []byte) ([]byte, []byte) {
		n := binary.LittleEndian.Uint32(b)
		return b[4 : 4+n], b[4+n:]
	}
	signers, _ := read(v2)
	signer, _ := read(signers)
	signedData, rest := read(signer)
	sigs, rest := read(rest)
	spki, _ := read(rest)

	digests, sdRest := read(signedData)
	certs, _ := read(sdRest)
	digest, _ := read(digests)
	if binary.LittleEndian.Uint32(digest) != algRSAPKCS1SHA256 {
		t.Fatal("digest algorithm")
	}
	gotDigest, _ := read(digest[4:])
	certDER, _ := read(certs)
	sig, _ := read(sigs)
	if binary.LittleEndian.Uint32(sig) != algRSAPKCS1SHA256 {
		t.Fatal("signature algorithm")
	}
	sigBytes, _ := read(sig[4:])

	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := x509.ParsePKIXPublicKey(spki)
	if err != nil {
		t.Fatal(err)
	}
	if !pub.(*rsa.PublicKey).Equal(cert.PublicKey) {
		t.Fatal("public key does not match the certificate")
	}
	sum := sha256.Sum256(signedData)
	if err := rsa.VerifyPKCS1v15(pub.(*rsa.PublicKey), crypto.SHA256, sum[:], sigBytes); err != nil {
		t.Fatal("signature:", err)
	}
	eocd := append([]byte(nil), apk[l.eocdStart:]...)
	binary.LittleEndian.PutUint32(eocd[16:], uint32(l.entriesEnd))
	if !bytes.Equal(contentDigest(apk[:l.entriesEnd], apk[l.cdStart:l.eocdStart], eocd), gotDigest) {
		t.Fatal("content digest mismatch")
	}
	return certDER
}

func TestSignAndResign(t *testing.T) {
	dir := t.TempDir()
	a, created, err := LoadOrCreate(filepath.Join(dir, "a.pem"))
	if err != nil || !created {
		t.Fatalf("create: %v %v", created, err)
	}
	if st, _ := os.Stat(filepath.Join(dir, "a.pem")); st.Mode().Perm()&0o077 != 0 && os.PathSeparator == '/' {
		t.Fatalf("key file mode %v", st.Mode())
	}
	again, created, err := LoadOrCreate(filepath.Join(dir, "a.pem"))
	if err != nil || created || again.Fingerprint() != a.Fingerprint() {
		t.Fatal("reload gave a different key")
	}
	b, _, _ := LoadOrCreate(filepath.Join(dir, "b.pem"))

	src := testAPK(t)
	signed, err := a.Sign(src)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(verify(t, signed), a.cert) {
		t.Fatal("signed with the wrong certificate")
	}
	// Entries are untouched and the archive still reads.
	if !bytes.Equal(signed[:len(src)-len(src)+bytes.Index(src, []byte{0x50, 0x4b, 0x01, 0x02})], src[:bytes.Index(src, []byte{0x50, 0x4b, 0x01, 0x02})]) {
		t.Fatal("entries moved")
	}
	zr, err := zip.NewReader(bytes.NewReader(signed), int64(len(signed)))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.Copy(io.Discard, rc); err != nil {
			t.Fatalf("%s: %v", f.Name, err)
		}
		rc.Close()
	}
	// Signing is deterministic, and re-signing replaces the block rather
	// than stacking another one.
	if again, _ := a.Sign(src); !bytes.Equal(again, signed) {
		t.Fatal("signing the same input twice differs")
	}
	resigned, err := b.Sign(signed)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(verify(t, resigned), b.cert) || len(resigned) != len(signed) {
		t.Fatalf("re-sign: len %d vs %d", len(resigned), len(signed))
	}
	if back, _ := a.Sign(resigned); !bytes.Equal(back, signed) {
		t.Fatal("re-signing with the first key does not give the first result")
	}
	if HasJARSignature(src) {
		t.Fatal("false JAR signature")
	}
}

func TestRefusesJARSigned(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, n := range []string{"AndroidManifest.xml", "META-INF/CERT.RSA"} {
		w, _ := zw.Create(n)
		_, _ = w.Write([]byte("x"))
	}
	_ = zw.Close()
	s, _, _ := LoadOrCreate(filepath.Join(t.TempDir(), "k.pem"))
	if _, err := s.Sign(buf.Bytes()); err == nil {
		t.Fatal("a JAR-signed APK was re-signed")
	}
}

// TestSignFile signs a real APK for checking with Android's apksigner:
//
//	APKSIGN_IN=app.apk APKSIGN_OUT=signed.apk APKSIGN_KEY=k.pem go test ./internal/apksign -run TestSignFile
//	apksigner verify -v --print-certs signed.apk
func TestSignFile(t *testing.T) {
	in, out, key := os.Getenv("APKSIGN_IN"), os.Getenv("APKSIGN_OUT"), os.Getenv("APKSIGN_KEY")
	if in == "" || out == "" || key == "" {
		t.Skip("set APKSIGN_IN, APKSIGN_OUT and APKSIGN_KEY")
	}
	s, _, err := LoadOrCreate(key)
	if err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile(in)
	if err != nil {
		t.Fatal(err)
	}
	signed, err := s.Sign(src)
	if err != nil {
		t.Fatal(err)
	}
	verify(t, signed)
	if err := os.WriteFile(out, signed, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("signed %s -> %s with %s", in, out, s.Fingerprint())
}
