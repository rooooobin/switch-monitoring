package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	xiaoduAVTType = "urn:schemas-upnp-org:service:AVTransport:1"
	xiaoduRCType  = "urn:schemas-upnp-org:service:RenderingControl:1"
	xiaoduAVTURL  = "/upnp/control/rendertransport1"
	xiaoduRCURL   = "/upnp/control/rendercontrol1"
)

var (
	duerosUnifiedURL = "https://xiaodu.baidu.com/saiya/smarthome/unified"
	duerosDLPURL     = "https://dueros-h2.baidu.com/dlp/controller/send_to_server"
	baiduLocalTTSURL = "https://fanyi.baidu.com/gettts"
)

var (
	reCurrentVolume  = regexp.MustCompile(`<CurrentVolume>(\d+)</CurrentVolume>`)
	reCurrentMute    = regexp.MustCompile(`<CurrentMute>(\d+)</CurrentMute>`)
	reTransportState = regexp.MustCompile(`<CurrentTransportState>(\w+)</CurrentTransportState>`)
	reRelTime        = regexp.MustCompile(`<RelTime>(.*?)</RelTime>`)
	reTrackDuration  = regexp.MustCompile(`<TrackDuration>(.*?)</TrackDuration>`)
)

// XiaoduDuerOSConfig holds optional cloud API credentials for TTS and voice commands.
type XiaoduDuerOSConfig struct {
	ClientID string
	CUID     string
	BDUSS    string
	SceneID  string
}

// XiaoduClient controls a Xiaodu speaker via local DLNA and optional DuerOS cloud APIs.
type XiaoduClient struct {
	baseURL string
	dueros  XiaoduDuerOSConfig
	client  *http.Client
}

// NewXiaoduClient creates a client. ip/port target the speaker DLNA endpoint.
func NewXiaoduClient(ip string, port int, dueros XiaoduDuerOSConfig) *XiaoduClient {
	if port <= 0 {
		port = 49494
	}
	return &XiaoduClient{
		baseURL: fmt.Sprintf("http://%s:%d", strings.TrimSpace(ip), port),
		dueros:  dueros,
		client:  &http.Client{Timeout: 15 * time.Second},
	}
}

// XiaoduStatus is a snapshot of speaker state from DLNA queries.
type XiaoduStatus struct {
	IP             string
	Port           int
	Volume         int
	Muted          bool
	TransportState string
	Position       string
	Duration       string
	DuerOSReady    bool
	DuerOSScene    bool
}

func (c *XiaoduClient) soapRequest(ctx context.Context, controlURL, action, serviceType, argsXML string) (string, error) {
	body := fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?>
<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/">
  <s:Body>
    <u:%s xmlns:u="%s">
      %s
    </u:%s>
  </s:Body>
</s:Envelope>`, action, serviceType, argsXML, action)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+controlURL, bytes.NewReader([]byte(body)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", `text/xml; charset="utf-8"`)
	req.Header.Set("SOAPAction", fmt.Sprintf(`"%s#%s"`, serviceType, action))

	slog.Info("Xiaodu DLNA request", "action", action, "url", c.baseURL+controlURL)
	resp, err := c.client.Do(req)
	if err != nil {
		slog.Error("Xiaodu DLNA transport error", "action", action, "err", err)
		return "", err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("dlna %s HTTP %d: %s", action, resp.StatusCode, truncate(string(raw), 200))
	}
	return string(raw), nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func firstMatch(re *regexp.Regexp, s string) string {
	m := re.FindStringSubmatch(s)
	if len(m) > 1 {
		return m[1]
	}
	return ""
}

// GetVolume returns current volume 0-100.
func (c *XiaoduClient) GetVolume(ctx context.Context) (int, error) {
	raw, err := c.soapRequest(ctx, xiaoduRCURL, "GetVolume", xiaoduRCType,
		"<InstanceID>0</InstanceID><Channel>Master</Channel>")
	if err != nil {
		return 0, err
	}
	v := firstMatch(reCurrentVolume, raw)
	if v == "" {
		return 0, fmt.Errorf("volume not found in DLNA response")
	}
	return strconv.Atoi(v)
}

// SetVolume sets volume 0-100.
func (c *XiaoduClient) SetVolume(ctx context.Context, volume int) (int, error) {
	if volume < 0 {
		volume = 0
	}
	if volume > 100 {
		volume = 100
	}
	_, err := c.soapRequest(ctx, xiaoduRCURL, "SetVolume", xiaoduRCType,
		fmt.Sprintf("<InstanceID>0</InstanceID><Channel>Master</Channel><DesiredVolume>%d</DesiredVolume>", volume))
	return volume, err
}

// GetMute returns whether the speaker is muted.
func (c *XiaoduClient) GetMute(ctx context.Context) (bool, error) {
	raw, err := c.soapRequest(ctx, xiaoduRCURL, "GetMute", xiaoduRCType,
		"<InstanceID>0</InstanceID><Channel>Master</Channel>")
	if err != nil {
		return false, err
	}
	return firstMatch(reCurrentMute, raw) == "1", nil
}

// SetMute sets mute on or off.
func (c *XiaoduClient) SetMute(ctx context.Context, muted bool) error {
	val := "0"
	if muted {
		val = "1"
	}
	_, err := c.soapRequest(ctx, xiaoduRCURL, "SetMute", xiaoduRCType,
		fmt.Sprintf("<InstanceID>0</InstanceID><Channel>Master</Channel><DesiredMute>%s</DesiredMute>", val))
	return err
}

// GetTransportState returns DLNA transport state (PLAYING, STOPPED, PAUSED_PLAYBACK, etc.).
func (c *XiaoduClient) GetTransportState(ctx context.Context) (string, error) {
	raw, err := c.soapRequest(ctx, xiaoduAVTURL, "GetTransportInfo", xiaoduAVTType, "<InstanceID>0</InstanceID>")
	if err != nil {
		return "", err
	}
	state := firstMatch(reTransportState, raw)
	if state == "" {
		return "UNKNOWN", nil
	}
	return state, nil
}

// GetPosition returns current playback position and track duration.
func (c *XiaoduClient) GetPosition(ctx context.Context) (position, duration string, err error) {
	raw, err := c.soapRequest(ctx, xiaoduAVTURL, "GetPositionInfo", xiaoduAVTType, "<InstanceID>0</InstanceID>")
	if err != nil {
		return "", "", err
	}
	pos := firstMatch(reRelTime, raw)
	dur := firstMatch(reTrackDuration, raw)
	if pos == "" {
		pos = "0:00:00"
	}
	if dur == "" {
		dur = "0:00:00"
	}
	return pos, dur, nil
}

// GetStatus aggregates DLNA status fields.
func (c *XiaoduClient) GetStatus(ctx context.Context) (*XiaoduStatus, error) {
	vol, err := c.GetVolume(ctx)
	if err != nil {
		return nil, err
	}
	mute, err := c.GetMute(ctx)
	if err != nil {
		return nil, err
	}
	state, err := c.GetTransportState(ctx)
	if err != nil {
		return nil, err
	}
	pos, dur, err := c.GetPosition(ctx)
	if err != nil {
		return nil, err
	}
	host := strings.TrimPrefix(strings.TrimPrefix(c.baseURL, "http://"), "https://")
	parts := strings.Split(host, ":")
	ip := parts[0]
	port := 49494
	if len(parts) > 1 {
		port, _ = strconv.Atoi(parts[1])
	}
	ready, scene := c.duerosConfigState()
	return &XiaoduStatus{
		IP:             ip,
		Port:           port,
		Volume:         vol,
		Muted:          mute,
		TransportState: state,
		Position:       pos,
		Duration:       dur,
		DuerOSReady:    ready,
		DuerOSScene:    scene,
	}, nil
}

func (c *XiaoduClient) duerosConfigState() (ready, sceneReady bool) {
	d := c.dueros
	hasBase := strings.TrimSpace(d.ClientID) != "" && strings.TrimSpace(d.CUID) != "" && strings.TrimSpace(d.BDUSS) != ""
	sceneReady = hasBase && strings.TrimSpace(d.SceneID) != ""
	return hasBase, sceneReady
}

func audioMIME(u string) string {
	lower := strings.ToLower(u)
	switch {
	case strings.HasSuffix(lower, ".mp3"):
		return "audio/mpeg"
	case strings.HasSuffix(lower, ".m4a"):
		return "audio/mp4"
	case strings.HasSuffix(lower, ".wav"):
		return "audio/wav"
	case strings.HasSuffix(lower, ".flac"):
		return "audio/flac"
	case strings.HasSuffix(lower, ".ogg"):
		return "audio/ogg"
	default:
		return "audio/mpeg"
	}
}

func xmlEscapeAttr(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, `"`, "&quot;")
	return s
}

// PlayURL plays a network audio URL via DLNA.
func (c *XiaoduClient) PlayURL(ctx context.Context, rawURL, title string) error {
	if strings.TrimSpace(rawURL) == "" {
		return fmt.Errorf("play url is empty")
	}
	if title == "" {
		title = "Audio"
	}
	mime := audioMIME(rawURL)
	urlXML := strings.ReplaceAll(rawURL, "&", "&amp;")
	urlInMeta := strings.ReplaceAll(rawURL, "&", "&amp;")
	meta := fmt.Sprintf(
		`&lt;DIDL-Lite xmlns=&quot;urn:schemas-upnp-org:metadata-1-0/DIDL-Lite/&quot;&gt;`+
			`&lt;item id=&quot;1&quot; parentID=&quot;0&quot; restricted=&quot;1&quot;&gt;`+
			`&lt;res protocolInfo=&quot;http-get:*:%s:*&quot;&gt;%s&lt;/res&gt;`+
			`&lt;title&gt;%s&lt;/title&gt;`+
			`&lt;upnp:class&gt;object.item.audioItem.musicTrack&lt;/upnp:class&gt;`+
			`&lt;/item&gt;&lt;/DIDL-Lite&gt;`,
		mime, urlInMeta, xmlEscapeAttr(title))

	_, err := c.soapRequest(ctx, xiaoduAVTURL, "SetAVTransportURI", xiaoduAVTType,
		fmt.Sprintf("<InstanceID>0</InstanceID><CurrentURI>%s</CurrentURI><CurrentURIMetaData>%s</CurrentURIMetaData>", urlXML, meta))
	if err != nil {
		return err
	}
	_, err = c.soapRequest(ctx, xiaoduAVTURL, "Play", xiaoduAVTType, "<InstanceID>0</InstanceID><Speed>1</Speed>")
	return err
}

// Stop stops playback.
func (c *XiaoduClient) Stop(ctx context.Context) error {
	_, err := c.soapRequest(ctx, xiaoduAVTURL, "Stop", xiaoduAVTType, "<InstanceID>0</InstanceID>")
	return err
}

// Pause pauses playback.
func (c *XiaoduClient) Pause(ctx context.Context) error {
	_, err := c.soapRequest(ctx, xiaoduAVTURL, "Pause", xiaoduAVTType, "<InstanceID>0</InstanceID>")
	return err
}

// Seek jumps to REL_TIME position (HH:MM:SS or seconds).
func (c *XiaoduClient) Seek(ctx context.Context, position string) error {
	target := position
	if n, err := strconv.Atoi(position); err == nil {
		target = fmt.Sprintf("%d:%02d:%02d", n/3600, (n%3600)/60, n%60)
	}
	_, err := c.soapRequest(ctx, xiaoduAVTURL, "Seek", xiaoduAVTType,
		fmt.Sprintf("<InstanceID>0</InstanceID><Unit>REL_TIME</Unit><Target>%s</Target>", target))
	return err
}

type duerosEnvelope struct {
	Status int    `json:"status"`
	Msg    string `json:"msg"`
}

func checkDuerOSResponse(body []byte) error {
	if len(bytes.TrimSpace(body)) == 0 {
		return nil
	}
	var env duerosEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil
	}
	switch env.Status {
	case 0:
		return nil
	case 2:
		return fmt.Errorf("dueros not logged in (BDUSS expired or invalid)")
	default:
		msg := env.Msg
		if msg == "" {
			msg = "unknown error"
		}
		return fmt.Errorf("dueros API error (status=%d): %s", env.Status, msg)
	}
}

func (c *XiaoduClient) duerosPost(ctx context.Context, endpoint string, payload any, extraHeaders map[string]string) ([]byte, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.dueros.BDUSS != "" {
		req.Header.Set("Cookie", "BDUSS="+c.dueros.BDUSS)
	}
	for k, v := range extraHeaders {
		req.Header.Set(k, v)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("dueros HTTP %d: %s", resp.StatusCode, truncate(string(raw), 200))
	}
	if err := checkDuerOSResponse(raw); err != nil {
		return raw, err
	}
	return raw, nil
}

func (c *XiaoduClient) duerosSceneCall(ctx context.Context, blockType, textField, text string) error {
	ready, scene := c.duerosConfigState()
	if !ready || !scene {
		return fmt.Errorf("dueros scene API requires client_id, cuid, bduss, and scene_id")
	}
	updatePayload := map[string]any{
		"method":  "updateScene",
		"from":    "h5",
		"version": "2.0",
		"params": map[string]any{
			"sceneId": c.dueros.SceneID,
			"cuid":    c.dueros.CUID,
			"sceneBlocks": []map[string]any{{
				"blockType": blockType,
				textField:   text,
				"cuid":      c.dueros.CUID,
				"clientId":  c.dueros.ClientID,
			}},
			"clientId": c.dueros.ClientID,
		},
	}
	if _, err := c.duerosPost(ctx, duerosUnifiedURL, updatePayload, nil); err != nil {
		return fmt.Errorf("update scene: %w", err)
	}
	triggerPayload := map[string]any{
		"method": "triggerScene",
		"params": map[string]string{"sceneId": c.dueros.SceneID},
	}
	_, err := c.duerosPost(ctx, duerosUnifiedURL, triggerPayload, nil)
	return err
}

// DuerOSDirectQuery sends a voice command via Direct DLP API.
func (c *XiaoduClient) DuerOSDirectQuery(ctx context.Context, text string) error {
	ready, _ := c.duerosConfigState()
	if !ready {
		return fmt.Errorf("dueros requires client_id, cuid, and bduss")
	}
	payload := map[string]any{
		"to_server": map[string]any{
			"header": map[string]string{
				"dialogRequestId": "",
				"messageId":       "",
				"name":            "LinkClicked",
				"namespace":       "dlp.screen",
			},
			"payload": map[string]any{
				"initiator": map[string]string{"type": "USER_CLICK"},
				"token":     "",
				"url":       "dueros://server.dueros.ai/query?q=" + url.QueryEscape(text),
			},
		},
		"uuid": "",
	}
	_, err := c.duerosPost(ctx, duerosDLPURL, payload, map[string]string{
		"client_id":        c.dueros.ClientID,
		"dueros-device-id": c.dueros.CUID,
	})
	return err
}

// DuerOSTTS speaks text using the DuerOS scene broadcastTTS API.
func (c *XiaoduClient) DuerOSTTS(ctx context.Context, text string) error {
	return c.duerosSceneCall(ctx, "broadcastTTS", "tts", text)
}

// LocalTTS generates audio via Baidu translate TTS and plays it through DLNA.
func (c *XiaoduClient) LocalTTS(ctx context.Context, text string) error {
	ttsURL := baiduLocalTTSURL + "?lan=zh&text=" + url.QueryEscape(text) + "&spd=5&source=web"
	title := text
	if len([]rune(title)) > 30 {
		title = string([]rune(title)[:30])
	}
	return c.PlayURL(ctx, ttsURL, title)
}

// TTS speaks text, preferring DuerOS native TTS with local TTS fallback.
func (c *XiaoduClient) TTS(ctx context.Context, text string) (used string, err error) {
	_, scene := c.duerosConfigState()
	if scene {
		if err := c.DuerOSTTS(ctx, text); err == nil {
			return "dueros", nil
		}
		slog.Warn("Xiaodu DuerOS TTS failed, falling back to local TTS", "err", err)
	}
	if err := c.LocalTTS(ctx, text); err != nil {
		return "", err
	}
	return "local", nil
}

// Say sends a voice command. Tries Direct DLP, then scene API, then local TTS fallback.
func (c *XiaoduClient) Say(ctx context.Context, text string) (used string, err error) {
	ready, scene := c.duerosConfigState()
	if ready {
		if err := c.DuerOSDirectQuery(ctx, text); err == nil {
			return "dlp", nil
		}
		slog.Warn("Xiaodu Direct DLP failed", "err", err)
	}
	if scene {
		if err := c.duerosSceneCall(ctx, "customQuery", "content", text); err == nil {
			return "scene", nil
		}
		slog.Warn("Xiaodu scene query failed", "err", err)
	}
	if err := c.LocalTTS(ctx, text); err != nil {
		return "", err
	}
	return "local", nil
}
