package adapter

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1" // Test-only: signs fixtures the way DuerOS does. See duerosSignatureHash.
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

const testCertURL = "https://duer.bdstatic.com/saiya/flow/dueros-api.baidu.com.cer.txt"

// newDuerOSTestCert builds a self-signed certificate with the given SANs, plus
// the matching private key so tests can forge valid signatures.
func newDuerOSTestCert(t *testing.T, sans ...string) (*rsa.PrivateKey, []byte) {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(20260101),
		Subject:      pkix.Name{CommonName: "dueros-api.baidu.com"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		DNSNames:     sans,
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	return priv, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// signDuerOSBody reproduces the platform's signing step: RSA-SHA1 over the raw body.
func signDuerOSBody(t *testing.T, priv *rsa.PrivateKey, body []byte) string {
	t.Helper()
	digest := sha1.Sum(body) // protocol-mandated; see duerosSignatureHash.
	sig, err := rsa.SignPKCS1v15(rand.Reader, priv, crypto.SHA1, digest[:])
	if err != nil {
		t.Fatalf("sign body: %v", err)
	}
	return base64.StdEncoding.EncodeToString(sig)
}

func intentRequestJSON(t *testing.T, text string) []byte {
	t.Helper()
	body := []byte(`{
	  "version": "2.0",
	  "session": {"new": false, "sessionId": "s-1", "application": {"applicationId": "bot-42"}},
	  "context": {"System": {"user": {"userId": "u-7"}, "device": {"deviceId": "dev-9"}}},
	  "request": {
	    "type": "IntentRequest",
	    "requestId": "r-1",
	    "query": {"type": "TEXT", "original": "` + text + `"},
	    "intents": [{"name": "ai.dueros.common.default_intent",
	               "slots": {"query": {"name": "query", "value": "` + text + `"}}}]
	  }
	}`)
	return body
}

func TestParseDuerOSRequest(t *testing.T) {
	t.Run("launch", func(t *testing.T) {
		raw := []byte(`{"version":"2.0","session":{"new":true,"sessionId":"s"},
			"request":{"type":"LaunchRequest","requestId":"r","timestamp":"1499169543"}}`)
		req, err := ParseDuerOSRequest(raw)
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if req.Request.Type != DuerOSTypeLaunch {
			t.Errorf("type = %q", req.Request.Type)
		}
		if !req.Session.New {
			t.Error("session.new should be true for a launch")
		}
		ts, ok := req.TimestampTime()
		if !ok || ts.Unix() != 1499169543 {
			t.Errorf("timestamp = %v (%v)", ts, ok)
		}
	})

	t.Run("plural intents", func(t *testing.T) {
		req, err := ParseDuerOSRequest(intentRequestJSON(t, "今天天气怎么样"))
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if got := len(req.Intents()); got != 1 {
			t.Fatalf("intents = %d, want 1", got)
		}
		if req.Text() != "今天天气怎么样" {
			t.Errorf("Text() = %q", req.Text())
		}
		if req.ApplicationID() != "bot-42" {
			t.Errorf("ApplicationID() = %q", req.ApplicationID())
		}
		if req.UserID() != "u-7" {
			t.Errorf("UserID() = %q", req.UserID())
		}
		if req.DeviceID() != "dev-9" {
			t.Errorf("DeviceID() = %q", req.DeviceID())
		}
	})

	t.Run("singular intent form is tolerated", func(t *testing.T) {
		raw := []byte(`{"request":{"type":"IntentRequest","intent":{"name":"AskLLMIntent",
			"slots":{"query":{"name":"query","value":"你好"}}}}}`)
		req, err := ParseDuerOSRequest(raw)
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if got := len(req.Intents()); got != 1 {
			t.Fatalf("intents = %d, want the singular form normalised to 1", got)
		}
		if req.Text() != "你好" {
			t.Errorf("Text() = %q", req.Text())
		}
	})

	t.Run("session ended", func(t *testing.T) {
		raw := []byte(`{"request":{"type":"SessionEndedRequest","reason":"USER_INITIATED",
			"error":{"type":"E","message":"m"}}}`)
		req, err := ParseDuerOSRequest(raw)
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if req.Request.Reason != DuerOSEndUserInitiated {
			t.Errorf("reason = %q", req.Request.Reason)
		}
		if req.Request.Error == nil || req.Request.Error.Message != "m" {
			t.Errorf("error = %+v", req.Request.Error)
		}
	})

	t.Run("rejects empty and malformed bodies", func(t *testing.T) {
		if _, err := ParseDuerOSRequest([]byte("   ")); err == nil {
			t.Error("expected error for an empty body")
		}
		if _, err := ParseDuerOSRequest([]byte("{not json")); err == nil {
			t.Error("expected error for malformed JSON")
		}
		if _, err := ParseDuerOSRequest([]byte(`{"version":"2.0"}`)); err == nil {
			t.Error("expected error when request.type is missing")
		}
	})
}

func TestDuerOSRequestTextPriority(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "known slot wins over query.original",
			raw:  `{"request":{"type":"IntentRequest","query":{"original":"打开助手 讲讲量子力学"},"intents":[{"slots":{"query":{"value":"讲讲量子力学"}}}]}}`,
			want: "讲讲量子力学",
		},
		{
			name: "custom slot name falls back to all slots",
			raw:  `{"request":{"type":"IntentRequest","intents":[{"slots":{"zzz":{"value":"北京"},"aaa":{"value":"上海"}}}]}}`,
			want: "上海 北京",
		},
		{
			name: "slot values array is joined",
			raw:  `{"request":{"type":"IntentRequest","intents":[{"slots":{"text":{"values":["张学友","刘德华"]}}}]}}`,
			want: "张学友 刘德华",
		},
		{
			name: "rewritten preferred over original",
			raw:  `{"request":{"type":"IntentRequest","query":{"original":"原始","rewritten":"改写后"}}}`,
			want: "改写后",
		},
		{
			name: "original used when nothing else",
			raw:  `{"request":{"type":"IntentRequest","query":{"original":"只有原文"}}}`,
			want: "只有原文",
		},
		{
			name: "empty when no text anywhere",
			raw:  `{"request":{"type":"IntentRequest"}}`,
			want: "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req, err := ParseDuerOSRequest([]byte(tc.raw))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if got := req.Text(); got != tc.want {
				t.Errorf("Text() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestDuerOSRequestTextSlotOrderIsStable guards against Go's randomised map
// iteration leaking into the fallback path, which would make replies vary between
// identical requests. Slots are joined in slot-name order.
func TestDuerOSRequestTextSlotOrderIsStable(t *testing.T) {
	raw := []byte(`{"request":{"type":"IntentRequest","intents":[{"slots":{
		"s1":{"value":"one"},"s2":{"value":"two"},"s3":{"value":"three"},
		"s4":{"value":"four"},"s5":{"value":"five"},"s6":{"value":"six"}}}]}}`)
	first := ""
	for i := 0; i < 40; i++ {
		req, err := ParseDuerOSRequest(raw)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		got := req.Text()
		if i == 0 {
			first = got
			if want := "one two three four five six"; got != want {
				t.Errorf("Text() = %q, want slot-name order %q", got, want)
			}
			continue
		}
		if got != first {
			t.Fatalf("Text() is not deterministic: %q then %q", first, got)
		}
	}
}

func TestDuerOSTimestampTime(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantOK  bool
		wantSec int64
	}{
		{"integer seconds", "1499169543", true, 1499169543},
		{"fractional seconds", "1499169543.500", true, 1499169543},
		{"empty", "", false, 0},
		{"garbage", "not-a-number", false, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := &DuerOSRequest{}
			req.Request.Timestamp = tc.raw
			got, ok := req.TimestampTime()
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if ok && got.Unix() != tc.wantSec {
				t.Errorf("unix = %d, want %d", got.Unix(), tc.wantSec)
			}
		})
	}
}

func TestValidateDuerOSCertURL(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{"canonical url", testCertURL, false},
		{"explicit 443", "https://duer.bdstatic.com:443/saiya/flow/x.cer.txt", false},
		{"http scheme", "http://duer.bdstatic.com/saiya/flow/x.cer.txt", true},
		{"wrong host", "https://evil.example.com/saiya/flow/x.cer.txt", true},
		{"host as suffix", "https://notduer.bdstatic.com/saiya/flow/x.cer.txt", true},
		{"wrong path prefix", "https://duer.bdstatic.com/other/x.cer.txt", true},
		{"path prefix case matters", "https://duer.bdstatic.com/Saiya/Flow/x.cer.txt", true},
		{"non-443 port", "https://duer.bdstatic.com:8443/saiya/flow/x.cer.txt", true},
		{"unparsable", "://nope", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateDuerOSCertURL(tc.url)
			if (err != nil) != tc.wantErr {
				t.Errorf("validateDuerOSCertURL(%q) err = %v, wantErr %v", tc.url, err, tc.wantErr)
			}
		})
	}
}

func TestParseDuerOSCert(t *testing.T) {
	t.Run("accepts the pinned SAN", func(t *testing.T) {
		_, pemBytes := newDuerOSTestCert(t, "dueros-api.baidu.com")
		cert, err := parseDuerOSCert(pemBytes)
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if cert.Subject.CommonName != "dueros-api.baidu.com" {
			t.Errorf("CN = %q", cert.Subject.CommonName)
		}
	})

	t.Run("rejects a certificate for another name", func(t *testing.T) {
		_, pemBytes := newDuerOSTestCert(t, "attacker.example.com")
		if _, err := parseDuerOSCert(pemBytes); err == nil {
			t.Fatal("expected the SAN check to reject this certificate")
		}
	})

	t.Run("rejects non-PEM input", func(t *testing.T) {
		if _, err := parseDuerOSCert([]byte("garbage")); err == nil {
			t.Fatal("expected error for non-PEM data")
		}
	})
}

func newTestVerifier(pemBytes []byte, fetches *int) *DuerOSSignatureVerifier {
	v := NewDuerOSSignatureVerifier()
	v.FetchCert = func(context.Context, string) ([]byte, error) {
		if fetches != nil {
			*fetches++
		}
		return pemBytes, nil
	}
	return v
}

func TestDuerOSSignatureVerify(t *testing.T) {
	priv, pemBytes := newDuerOSTestCert(t, "dueros-api.baidu.com")

	t.Run("accepts a valid signature", func(t *testing.T) {
		body := intentRequestJSON(t, "现在几点了")
		req := signedDuerOSTestRequest(t, body, 0)
		raw, _ := json.Marshal(req)

		header := http.Header{}
		header.Set("signaturecerturl", testCertURL)
		header.Set("signature", signDuerOSBody(t, priv, raw))

		v := newTestVerifier(pemBytes, nil)
		if err := v.Verify(context.Background(), raw, header, req); err != nil {
			t.Fatalf("Verify: %v", err)
		}
	})

	t.Run("rejects a tampered body", func(t *testing.T) {
		body := intentRequestJSON(t, "现在几点了")
		req := signedDuerOSTestRequest(t, body, 0)
		raw, _ := json.Marshal(req)

		header := http.Header{}
		header.Set("signaturecerturl", testCertURL)
		header.Set("signature", signDuerOSBody(t, priv, raw))

		tampered := []byte(strings.Replace(string(raw), "现在几点了", "转账一万元", 1))
		v := newTestVerifier(pemBytes, nil)
		if err := v.Verify(context.Background(), tampered, header, req); err == nil {
			t.Fatal("expected verification to fail for a tampered body")
		}
	})

	t.Run("rejects a signature from another key", func(t *testing.T) {
		other, _ := newDuerOSTestCert(t, "dueros-api.baidu.com")
		body := intentRequestJSON(t, "hi")
		req := signedDuerOSTestRequest(t, body, 0)
		raw, _ := json.Marshal(req)

		header := http.Header{}
		header.Set("signaturecerturl", testCertURL)
		header.Set("signature", signDuerOSBody(t, other, raw))

		v := newTestVerifier(pemBytes, nil)
		if err := v.Verify(context.Background(), raw, header, req); err == nil {
			t.Fatal("expected verification to fail for a foreign key")
		}
	})

	t.Run("rejects a stale timestamp", func(t *testing.T) {
		body := intentRequestJSON(t, "hi")
		req := signedDuerOSTestRequest(t, body, 10*time.Minute)
		raw, _ := json.Marshal(req)

		header := http.Header{}
		header.Set("signaturecerturl", testCertURL)
		header.Set("signature", signDuerOSBody(t, priv, raw))

		v := newTestVerifier(pemBytes, nil)
		err := v.Verify(context.Background(), raw, header, req)
		if err == nil {
			t.Fatal("expected a stale timestamp to be rejected")
		}
		if !strings.Contains(err.Error(), "skew") {
			t.Errorf("error %q should mention skew", err)
		}
	})

	t.Run("rejects missing headers", func(t *testing.T) {
		v := newTestVerifier(pemBytes, nil)
		if err := v.Verify(context.Background(), []byte(`{}`), http.Header{}, nil); err == nil {
			t.Fatal("expected an error without signaturecerturl/signature")
		}
	})

	t.Run("rejects an unparsable cert url before any fetch", func(t *testing.T) {
		fetches := 0
		v := newTestVerifier(pemBytes, &fetches)
		header := http.Header{}
		header.Set("signaturecerturl", "https://evil.example.com/saiya/flow/x.cer.txt")
		header.Set("signature", "AAAA")
		if err := v.Verify(context.Background(), []byte(`{}`), header, nil); err == nil {
			t.Fatal("expected the host pin to reject this URL")
		}
		if fetches != 0 {
			t.Errorf("fetcher ran %d times; the URL must be rejected before any request", fetches)
		}
	})

	t.Run("rejects a certificate without the pinned SAN", func(t *testing.T) {
		_, badPEM := newDuerOSTestCert(t, "attacker.example.com")
		v := newTestVerifier(badPEM, nil)
		header := http.Header{}
		header.Set("signaturecerturl", testCertURL)
		header.Set("signature", "AAAA")
		if err := v.Verify(context.Background(), []byte(`{}`), header, nil); err == nil {
			t.Fatal("expected the SAN check to reject this certificate")
		}
	})

	t.Run("rejects a non-base64 signature", func(t *testing.T) {
		body := intentRequestJSON(t, "hi")
		req := signedDuerOSTestRequest(t, body, 0)
		raw, _ := json.Marshal(req)
		header := http.Header{}
		header.Set("signaturecerturl", testCertURL)
		header.Set("signature", "!!!not base64!!!")
		v := newTestVerifier(pemBytes, nil)
		if err := v.Verify(context.Background(), raw, header, req); err == nil {
			t.Fatal("expected an error for a non-base64 signature")
		}
	})
}

// TestDuerOSSignatureVerifyCachesCert ensures the certificate is fetched once and
// reused: a fetch per voice turn would add a round trip to every reply.
func TestDuerOSSignatureVerifyCachesCert(t *testing.T) {
	priv, pemBytes := newDuerOSTestCert(t, "dueros-api.baidu.com")
	fetches := 0
	v := newTestVerifier(pemBytes, &fetches)

	for i := 0; i < 3; i++ {
		body := intentRequestJSON(t, "hi")
		req := signedDuerOSTestRequest(t, body, 0)
		raw, _ := json.Marshal(req)
		header := http.Header{}
		header.Set("signaturecerturl", testCertURL)
		header.Set("signature", signDuerOSBody(t, priv, raw))
		if err := v.Verify(context.Background(), raw, header, req); err != nil {
			t.Fatalf("verify %d: %v", i, err)
		}
	}
	if fetches != 1 {
		t.Errorf("cert fetched %d times, want 1 (cached)", fetches)
	}
}

// signedDuerOSTestRequest re-encodes a fixture so the signed bytes carry the
// timestamp the verifier will check.
func signedDuerOSTestRequest(t *testing.T, body []byte, age time.Duration) *DuerOSRequest {
	t.Helper()
	req, err := ParseDuerOSRequest(body)
	if err != nil {
		t.Fatalf("ParseDuerOSRequest: %v", err)
	}
	req.Request.Timestamp = strconv.FormatInt(time.Now().Add(-age).Unix(), 10)
	return req
}

func TestNewDuerOSReply(t *testing.T) {
	t.Run("open session carries a reprompt", func(t *testing.T) {
		resp := NewDuerOSReply("答案是四十二。", skillRepromptFixture, nil)
		if resp.Version != "2.0" {
			t.Errorf("version = %q", resp.Version)
		}
		if resp.Response.ShouldEndSession {
			t.Error("shouldEndSession must be false while a reprompt is set")
		}
		if resp.Response.OutputSpeech == nil || resp.Response.OutputSpeech.Type != "PlainText" {
			t.Fatalf("outputSpeech = %+v", resp.Response.OutputSpeech)
		}
		if resp.Response.OutputSpeech.Text != "答案是四十二。" {
			t.Errorf("text = %q", resp.Response.OutputSpeech.Text)
		}
		if resp.Response.Reprompt == nil || resp.Response.Reprompt.OutputSpeech == nil {
			t.Fatal("reprompt missing: the device would close the microphone")
		}
		if resp.Response.ExpectSpeech == nil || !*resp.Response.ExpectSpeech {
			t.Error("expectSpeech should be true for an open session")
		}

		raw, err := json.Marshal(resp)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		for _, want := range []string{`"shouldEndSession":false`, `"outputSpeech"`, `"reprompt"`, `"expectSpeech":true`} {
			if !strings.Contains(string(raw), want) {
				t.Errorf("wire format missing %s: %s", want, raw)
			}
		}
	})

	t.Run("empty reprompt ends the session", func(t *testing.T) {
		resp := NewDuerOSReply("再见。", "  ", nil)
		if !resp.Response.ShouldEndSession {
			t.Error("shouldEndSession must be true without a reprompt")
		}
		if resp.Response.Reprompt != nil {
			t.Error("reprompt should be omitted")
		}
	})

	t.Run("attributes are echoed", func(t *testing.T) {
		resp := NewDuerOSReply("ok", "", map[string]string{"k": "v"})
		if resp.Session == nil || resp.Session.Attributes["k"] != "v" {
			t.Errorf("session attributes = %+v", resp.Session)
		}
	})
}

func TestNewDuerOSGoodbye(t *testing.T) {
	resp := NewDuerOSGoodbye("好的，再见。")
	if !resp.Response.ShouldEndSession {
		t.Error("a goodbye must end the session")
	}
	if resp.Response.OutputSpeech.Text != "好的，再见。" {
		t.Errorf("text = %q", resp.Response.OutputSpeech.Text)
	}
	if resp.Response.Reprompt != nil {
		t.Error("a goodbye must not carry a reprompt")
	}

	// SessionEndedRequest passes no text: the response must then omit outputSpeech
	// rather than send an empty PlainText the device would try to read aloud.
	silent := NewDuerOSGoodbye("")
	if silent.Response.OutputSpeech != nil {
		t.Errorf("outputSpeech = %+v, want nil", silent.Response.OutputSpeech)
	}
	if !silent.Response.ShouldEndSession {
		t.Error("a silent goodbye must still end the session")
	}
	raw, err := json.Marshal(silent)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), "outputSpeech") {
		t.Errorf("wire format should omit outputSpeech: %s", raw)
	}
}

// skillRepromptFixture keeps this file independent of the runner package.
const skillRepromptFixture = "我在听，请说。"
