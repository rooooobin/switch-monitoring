package adapter

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newXiaoduTestClient(t *testing.T, dlnaHandler http.HandlerFunc) (*XiaoduClient, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(dlnaHandler)
	host := strings.TrimPrefix(srv.URL, "http://")
	parts := strings.Split(host, ":")
	port := 80
	if len(parts) == 2 {
		port = mustAtoi(t, parts[1])
	}
	c := NewXiaoduClient(parts[0], port, XiaoduDuerOSConfig{
		ClientID: "client1234567890123456789012345678",
		CUID:     "cuid1234567890ab",
		BDUSS:    "bduss-test",
		SceneID:  "scene1234567890abcdef",
	})
	return c, srv
}

func mustAtoi(t *testing.T, s string) int {
	t.Helper()
	n := 0
	for _, ch := range s {
		n = n*10 + int(ch-'0')
	}
	return n
}

func TestXiaoduGetStatus(t *testing.T) {
	c, srv := newXiaoduTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		action := extractSOAPAction(r.Header.Get("SOAPAction"))
		switch action {
		case "GetVolume":
			_, _ = w.Write([]byte(`<CurrentVolume>42</CurrentVolume>`))
		case "GetMute":
			_, _ = w.Write([]byte(`<CurrentMute>0</CurrentMute>`))
		case "GetTransportInfo":
			_, _ = w.Write([]byte(`<CurrentTransportState>PLAYING</CurrentTransportState>`))
		case "GetPositionInfo":
			_, _ = w.Write([]byte(`<RelTime>0:01:05</RelTime><TrackDuration>0:03:20</TrackDuration>`))
		default:
			t.Fatalf("unexpected action %s body=%s", action, string(body))
		}
	})
	defer srv.Close()

	st, err := c.GetStatus(context.Background())
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if st.Volume != 42 || st.Muted || st.TransportState != "PLAYING" {
		t.Fatalf("unexpected status: %+v", st)
	}
	if st.Position != "0:01:05" || st.Duration != "0:03:20" {
		t.Fatalf("position/duration: %s / %s", st.Position, st.Duration)
	}
}

func TestXiaoduSetVolumeAndMute(t *testing.T) {
	var setVolume, setMute string
	c, srv := newXiaoduTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		action := extractSOAPAction(r.Header.Get("SOAPAction"))
		switch action {
		case "SetVolume":
			setVolume = string(body)
		case "SetMute":
			setMute = string(body)
		default:
			t.Fatalf("unexpected action %s", action)
		}
		_, _ = w.Write([]byte(`<ok/>`))
	})
	defer srv.Close()

	got, err := c.SetVolume(context.Background(), 75)
	if err != nil || got != 75 {
		t.Fatalf("SetVolume: got=%d err=%v", got, err)
	}
	if !strings.Contains(setVolume, "<DesiredVolume>75</DesiredVolume>") {
		t.Fatalf("SetVolume body: %s", setVolume)
	}
	if err := c.SetMute(context.Background(), true); err != nil {
		t.Fatalf("SetMute: %v", err)
	}
	if !strings.Contains(setMute, "<DesiredMute>1</DesiredMute>") {
		t.Fatalf("SetMute body: %s", setMute)
	}
}

func TestXiaoduPlayURL(t *testing.T) {
	var actions []string
	c, srv := newXiaoduTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		actions = append(actions, extractSOAPAction(r.Header.Get("SOAPAction")))
		_, _ = w.Write([]byte(`<ok/>`))
	})
	defer srv.Close()

	if err := c.PlayURL(context.Background(), "http://example.com/song.mp3?x=1&y=2", "Song"); err != nil {
		t.Fatalf("PlayURL: %v", err)
	}
	if len(actions) != 2 || actions[0] != "SetAVTransportURI" || actions[1] != "Play" {
		t.Fatalf("actions=%v", actions)
	}
}

func TestXiaoduDuerOSResponseCheck(t *testing.T) {
	if err := checkDuerOSResponse([]byte(`{"status":0}`)); err != nil {
		t.Fatalf("check ok: %v", err)
	}
	if err := checkDuerOSResponse([]byte(`{"status":2,"msg":"not login"}`)); err == nil {
		t.Fatal("expected login error")
	}
}

func TestXiaoduTTSFallbackLocal(t *testing.T) {
	var playURL string
	c, srv := newXiaoduTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "SetAVTransportURI") {
			if i := strings.Index(string(body), "<CurrentURI>"); i >= 0 {
				rest := string(body)[i+len("<CurrentURI>"):]
				if j := strings.Index(rest, "</CurrentURI>"); j >= 0 {
					playURL = rest[:j]
				}
			}
		}
		_, _ = w.Write([]byte(`<ok/>`))
	})
	defer srv.Close()

	used, err := c.TTS(context.Background(), "你好")
	if err != nil {
		t.Fatalf("TTS: %v", err)
	}
	if used != "local" {
		t.Fatalf("used=%q want local", used)
	}
	if !strings.Contains(playURL, "fanyi.baidu.com/gettts") {
		t.Fatalf("play url=%q", playURL)
	}
}

func TestXiaoduSayFallbackLocal(t *testing.T) {
	c, srv := newXiaoduTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<ok/>`))
	})
	defer srv.Close()
	c.dueros = XiaoduDuerOSConfig{}

	used, err := c.Say(context.Background(), "现在几点了")
	if err != nil {
		t.Fatalf("Say: %v", err)
	}
	if used != "local" {
		t.Fatalf("used=%q want local", used)
	}
}

func TestXiaoduDuerOSSceneFlow(t *testing.T) {
	var methods []string
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		methods = append(methods, payload["method"].(string))
		if cookie := r.Header.Get("Cookie"); !strings.Contains(cookie, "BDUSS=bduss-test") {
			t.Fatalf("missing cookie: %q", cookie)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": 0})
	}))
	defer cloud.Close()

	c := NewXiaoduClient("127.0.0.1", 49494, XiaoduDuerOSConfig{
		ClientID: "client1234567890123456789012345678",
		CUID:     "cuid1234567890ab",
		BDUSS:    "bduss-test",
		SceneID:  "scene1234567890abcdef",
	})

	// Inject cloud URL for this test
	orig := duerosUnifiedURL
	duerosUnifiedURL = cloud.URL
	t.Cleanup(func() { duerosUnifiedURL = orig })

	if err := c.DuerOSTTS(context.Background(), "测试播报"); err != nil {
		t.Fatalf("DuerOSTTS: %v", err)
	}
	if len(methods) != 2 || methods[0] != "updateScene" || methods[1] != "triggerScene" {
		t.Fatalf("methods=%v", methods)
	}
}

func TestXiaoduDuerOSDirectQuery(t *testing.T) {
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("client_id") == "" || r.Header.Get("dueros-device-id") == "" {
			t.Fatalf("missing dlp headers")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer cloud.Close()

	c := NewXiaoduClient("127.0.0.1", 49494, XiaoduDuerOSConfig{
		ClientID: "client1234567890123456789012345678",
		CUID:     "cuid1234567890ab",
		BDUSS:    "bduss-test",
	})
	orig := duerosDLPURL
	duerosDLPURL = cloud.URL
	t.Cleanup(func() { duerosDLPURL = orig })

	if err := c.DuerOSDirectQuery(context.Background(), "播放音乐"); err != nil {
		t.Fatalf("DuerOSDirectQuery: %v", err)
	}
}

func extractSOAPAction(header string) string {
	header = strings.Trim(header, `"`)
	if i := strings.LastIndex(header, "#"); i >= 0 {
		return header[i+1:]
	}
	return header
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
