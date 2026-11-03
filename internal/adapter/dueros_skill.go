package adapter

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha1" // Protocol-mandated digest for DuerOS signature verification; see duerosSignatureHash.
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DuerOS request types sent to a custom-skill webhook.
const (
	DuerOSTypeLaunch     = "LaunchRequest"
	DuerOSTypeIntent     = "IntentRequest"
	DuerOSTypeSessionEnd = "SessionEndedRequest"
)

// DuerOS session-end reasons (SessionEndedRequest.reason).
const (
	DuerOSEndUserInitiated = "USER_INITIATED"
	DuerOSEndMaxToken      = "EXCEEDED_MAX_TOKEN"
	DuerOSEndError         = "ERROR"
)

// Constraints from the DuerOS "技能认证" spec.
const (
	duerosCertHost      = "duer.bdstatic.com"
	duerosCertPathPfx   = "/saiya/flow/"
	duerosCertSAN       = "dueros-api.baidu.com"
	duerosMaxClockSkew  = 180 * time.Second
	duerosCertCacheTTL  = 24 * time.Hour
	duerosProtocolVer   = "2.0"
	duerosSpeechPlain   = "PlainText"
	duerosCertFetchSize = 64 << 10
)

// DuerOSUser identifies the Baidu account driving the request.
type DuerOSUser struct {
	UserID      string `json:"userId"`
	AccessToken string `json:"accessToken"`
}

// DuerOSApplication identifies the skill (botId).
type DuerOSApplication struct {
	ApplicationID string `json:"applicationId"`
}

// DuerOSDevice identifies the speaker the voice came from.
type DuerOSDevice struct {
	DeviceID            string                     `json:"deviceId"`
	SupportedInterfaces map[string]json.RawMessage `json:"supportedInterfaces"`
}

// DuerOSSession carries per-conversation state. Attributes echoed back in the
// response survive for the rest of the session.
type DuerOSSession struct {
	New         bool               `json:"new"`
	SessionID   string             `json:"sessionId"`
	Attributes  map[string]string  `json:"attributes"`
	User        *DuerOSUser        `json:"user"`
	Application *DuerOSApplication `json:"application"`
}

// DuerOSContext mirrors context.System from the request.
type DuerOSContext struct {
	System struct {
		User        *DuerOSUser        `json:"user"`
		Application *DuerOSApplication `json:"application"`
		Device      *DuerOSDevice      `json:"device"`
	} `json:"System"`
}

// DuerOSQuery is the recognised utterance.
type DuerOSQuery struct {
	Type      string `json:"type"`
	Original  string `json:"original"`
	Rewritten string `json:"rewritten"`
}

// DuerOSSlot is one filled slot of an intent.
type DuerOSSlot struct {
	Name               string   `json:"name"`
	Value              string   `json:"value"`
	Values             []string `json:"values"`
	ConfirmationStatus string   `json:"confirmationStatus"`
}

// DuerOSIntent is one NLU interpretation of the utterance.
type DuerOSIntent struct {
	Name               string                `json:"name"`
	ConfirmationStatus string                `json:"confirmationStatus"`
	Slots              map[string]DuerOSSlot `json:"slots"`
}

// DuerOSRequestError accompanies SessionEndedRequest when reason is ERROR.
type DuerOSRequestError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// DuerOSInnerRequest is the request body proper.
//
// Both the singular "intent" and the documented plural "intents" are decoded:
// the public spec uses "intents", but payloads observed in the wild (and the
// platform's own simulator) sometimes send "intent".
type DuerOSInnerRequest struct {
	Type        string              `json:"type"`
	RequestID   string              `json:"requestId"`
	Timestamp   string              `json:"timestamp"`
	Query       *DuerOSQuery        `json:"query"`
	DialogState string              `json:"dialogState"`
	Intents     []DuerOSIntent      `json:"intents"`
	Intent      *DuerOSIntent       `json:"intent"`
	Reason      string              `json:"reason"`
	Error       *DuerOSRequestError `json:"error"`
}

// DuerOSRequest is a full webhook payload from the skill platform.
type DuerOSRequest struct {
	Version string             `json:"version"`
	Session DuerOSSession      `json:"session"`
	Context DuerOSContext      `json:"context"`
	Request DuerOSInnerRequest `json:"request"`
}

// ApplicationID returns the skill id, preferring the session copy.
func (r *DuerOSRequest) ApplicationID() string {
	if r.Session.Application != nil && strings.TrimSpace(r.Session.Application.ApplicationID) != "" {
		return r.Session.Application.ApplicationID
	}
	if r.Context.System.Application != nil {
		return r.Context.System.Application.ApplicationID
	}
	return ""
}

// UserID returns the end-user id, preferring the session copy.
func (r *DuerOSRequest) UserID() string {
	if r.Session.User != nil && strings.TrimSpace(r.Session.User.UserID) != "" {
		return r.Session.User.UserID
	}
	if r.Context.System.User != nil {
		return r.Context.System.User.UserID
	}
	return ""
}

// DeviceID returns the speaker id when the platform supplied one.
func (r *DuerOSRequest) DeviceID() string {
	if r.Context.System.Device != nil {
		return r.Context.System.Device.DeviceID
	}
	return ""
}

// Intents returns every intent in the payload, normalising the singular form.
func (r *DuerOSRequest) Intents() []DuerOSIntent {
	if len(r.Request.Intents) > 0 {
		return r.Request.Intents
	}
	if r.Request.Intent != nil {
		return []DuerOSIntent{*r.Request.Intent}
	}
	return nil
}

// slotPreferredOrder lists the slot names a free-text skill usually fills, in
// descending priority. A skill built on the default intent typically carries the
// whole utterance in one of these.
var slotPreferredOrder = []string{"query", "text", "content", "question", "keyword"}

// Text extracts what the user actually said.
//
// Resolution order:
//  1. a well-known free-text slot (query/text/content/question/keyword);
//  2. every other slot value, joined -- covers skills that name the slot
//     something custom;
//  3. query.rewritten, then query.original -- always present for the default
//     intent, and the only source when NLU matched nothing.
func (r *DuerOSRequest) Text() string {
	intents := r.Intents()

	for _, name := range slotPreferredOrder {
		for _, in := range intents {
			if v := slotText(in.Slots[name]); v != "" {
				return v
			}
		}
	}

	var joined []string
	for _, in := range intents {
		for _, name := range sortedSlotNames(in.Slots) {
			if v := slotText(in.Slots[name]); v != "" {
				joined = append(joined, v)
			}
		}
	}
	if len(joined) > 0 {
		return strings.TrimSpace(strings.Join(joined, " "))
	}

	if q := r.Request.Query; q != nil {
		if v := strings.TrimSpace(q.Rewritten); v != "" {
			return v
		}
		if v := strings.TrimSpace(q.Original); v != "" {
			return v
		}
	}
	return ""
}

func slotText(s DuerOSSlot) string {
	if v := strings.TrimSpace(s.Value); v != "" {
		return v
	}
	var parts []string
	for _, v := range s.Values {
		if v = strings.TrimSpace(v); v != "" {
			parts = append(parts, v)
		}
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}

// sortedSlotNames gives deterministic slot iteration, so a reply never depends
// on Go's randomised map order.
func sortedSlotNames(slots map[string]DuerOSSlot) []string {
	names := make([]string, 0, len(slots))
	for name := range slots {
		names = append(names, name)
	}
	sortStrings(names)
	return names
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// TimestampTime parses request.timestamp. The spec says "all digits, seconds",
// but the platform also emits fractional seconds, so parse as float first.
func (r *DuerOSRequest) TimestampTime() (time.Time, bool) {
	raw := strings.TrimSpace(r.Request.Timestamp)
	if raw == "" {
		return time.Time{}, false
	}
	if f, err := strconv.ParseFloat(raw, 64); err == nil {
		sec := int64(f)
		nsec := int64((f - float64(sec)) * float64(time.Second))
		return time.Unix(sec, nsec), true
	}
	if n, err := strconv.ParseInt(raw, 10, 64); err == nil {
		return time.Unix(n, 0), true
	}
	return time.Time{}, false
}

// DuerOSSpeech is spoken output. Text is plain text; SSML is used instead when
// prosody control is needed.
type DuerOSSpeech struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	SSML string `json:"ssml,omitempty"`
}

// DuerOSReprompt is what the device says when the user stays silent.
type DuerOSReprompt struct {
	OutputSpeech *DuerOSSpeech `json:"outputSpeech,omitempty"`
}

// DuerOSResponseSession echoes session attributes back to the platform.
type DuerOSResponseSession struct {
	Attributes map[string]string `json:"attributes,omitempty"`
}

// DuerOSResponseBody is the payload the device acts on.
type DuerOSResponseBody struct {
	OutputSpeech     *DuerOSSpeech   `json:"outputSpeech,omitempty"`
	Reprompt         *DuerOSReprompt `json:"reprompt,omitempty"`
	ExpectSpeech     *bool           `json:"expectSpeech,omitempty"`
	ShouldEndSession bool            `json:"shouldEndSession"`
}

// DuerOSResponse is the webhook's reply.
type DuerOSResponse struct {
	Version  string                 `json:"version"`
	Session  *DuerOSResponseSession `json:"session,omitempty"`
	Response DuerOSResponseBody     `json:"response"`
}

func plainSpeech(text string) *DuerOSSpeech {
	return &DuerOSSpeech{Type: duerosSpeechPlain, Text: text}
}

// NewDuerOSReply speaks text. When reprompt is non-empty the session stays open
// and the device keeps listening, which is what makes multi-turn chat work.
func NewDuerOSReply(text, reprompt string, attributes map[string]string) *DuerOSResponse {
	end := strings.TrimSpace(reprompt) == ""
	body := DuerOSResponseBody{
		OutputSpeech:     plainSpeech(text),
		ShouldEndSession: end,
	}
	if !end {
		body.Reprompt = &DuerOSReprompt{OutputSpeech: plainSpeech(reprompt)}
		expect := true
		body.ExpectSpeech = &expect
	}
	resp := &DuerOSResponse{Version: duerosProtocolVer, Response: body}
	if len(attributes) > 0 {
		resp.Session = &DuerOSResponseSession{Attributes: attributes}
	}
	return resp
}

// NewDuerOSGoodbye ends the session. An empty text omits outputSpeech entirely:
// SessionEndedRequest needs no announcement, and an empty PlainText object would
// make the device try to speak nothing.
func NewDuerOSGoodbye(text string) *DuerOSResponse {
	resp := &DuerOSResponse{
		Version:  duerosProtocolVer,
		Response: DuerOSResponseBody{ShouldEndSession: true},
	}
	if strings.TrimSpace(text) != "" {
		resp.Response.OutputSpeech = plainSpeech(text)
	}
	return resp
}

// DuerOSCertFetcher downloads a signing certificate. Injectable for tests.
type DuerOSCertFetcher func(ctx context.Context, rawURL string) ([]byte, error)

// DuerOSSignatureVerifier implements the request authentication mandated by the
// DuerOS 技能认证 spec: fetch the X.509 cert named by the signaturecerturl header,
// then RSA-SHA1 verify the signature header over the raw request body.
//
// Without this, anyone who learns the webhook URL can spend your LLM quota and
// inject replies into your speaker.
type DuerOSSignatureVerifier struct {
	FetchCert DuerOSCertFetcher
	Now       func() time.Time
	MaxSkew   time.Duration

	mu    sync.Mutex
	certs map[string]cachedDuerOSCert
}

type cachedDuerOSCert struct {
	cert *x509.Certificate
	at   time.Time
}

// NewDuerOSSignatureVerifier builds a verifier with the production defaults.
func NewDuerOSSignatureVerifier() *DuerOSSignatureVerifier {
	return &DuerOSSignatureVerifier{
		FetchCert: fetchDuerOSCert,
		Now:       time.Now,
		MaxSkew:   duerosMaxClockSkew,
		certs:     map[string]cachedDuerOSCert{},
	}
}

// Verify checks cert URL shape, cert identity, the RSA-SHA1 signature and the
// request timestamp. Any failure returns an error describing which step failed.
func (v *DuerOSSignatureVerifier) Verify(ctx context.Context, body []byte, header http.Header, req *DuerOSRequest) error {
	certURL := strings.TrimSpace(header.Get("signaturecerturl"))
	signature := strings.TrimSpace(header.Get("signature"))
	if certURL == "" || signature == "" {
		return fmt.Errorf("missing signaturecerturl or signature header")
	}

	cert, err := v.certificate(ctx, certURL)
	if err != nil {
		return err
	}

	sig, err := base64.StdEncoding.DecodeString(signature)
	if err != nil {
		return fmt.Errorf("signature is not valid base64: %w", err)
	}
	pub, ok := cert.PublicKey.(*rsa.PublicKey)
	if !ok {
		return fmt.Errorf("signing certificate does not carry an RSA public key")
	}
	digest := duerosBodyDigest(body)
	if err := rsa.VerifyPKCS1v15(pub, duerosSignatureHash, digest[:], sig); err != nil {
		return fmt.Errorf("signature verification failed: %w", err)
	}

	maxSkew := v.MaxSkew
	if maxSkew <= 0 {
		maxSkew = duerosMaxClockSkew
	}
	if req != nil {
		ts, ok := req.TimestampTime()
		if !ok {
			return fmt.Errorf("request timestamp %q is not parseable", req.Request.Timestamp)
		}
		now := time.Now()
		if v.Now != nil {
			now = v.Now()
		}
		skew := now.Sub(ts)
		if skew < 0 {
			skew = -skew
		}
		if skew > maxSkew {
			return fmt.Errorf("request timestamp skew %.0fs exceeds allowed %.0fs",
				skew.Seconds(), maxSkew.Seconds())
		}
	}
	return nil
}

// duerosSignatureHash and duerosBodyDigest implement the digest step of DuerOS
// request authentication.
//
// SHA-1 here is not a design choice: the DuerOS “技能认证” spec fixes RSA-SHA1 as
// the signature algorithm, so any stronger digest would simply never verify.
// The exposure is narrow and deliberate --
//   - we only ever *verify* a signature over a body Baidu chose, never sign one;
//   - the signing certificate is pinned by URL host, path prefix and SAN, so an
//     attacker cannot substitute their own key;
//   - SHA-1's practical weaknesses are collision attacks, which do not yield a
//     second-preimage forgery of an arbitrary body;
//   - the transport is TLS.
//
// Never reuse this helper outside DuerOS signature verification.
//
// Weak-hash scanners flag this file and cannot be satisfied by changing the
// algorithm: the hash is the counterparty's choice, not ours. The finding is
// recorded as a false positive against the rule's own premise ("for
// security-sensitive contexts" where sha256+ is available to pick).
const duerosSignatureHash = crypto.SHA1

// duerosBodyDigest computes the message digest a DuerOS signature covers.
//
//nolint:gosec // G401,G505: RSA-SHA1 is mandated by the DuerOS protocol spec.
func duerosBodyDigest(body []byte) [sha1.Size]byte {
	return sha1.Sum(body)
}

// certificate resolves and caches the signing cert, validating the URL and the
// cert identity on the way. Caching matters: the platform reuses one cert URL,
// and fetching it per request would add a round trip to every voice turn.
func (v *DuerOSSignatureVerifier) certificate(ctx context.Context, certURL string) (*x509.Certificate, error) {
	if err := validateDuerOSCertURL(certURL); err != nil {
		return nil, err
	}

	now := time.Now()
	if v.Now != nil {
		now = v.Now()
	}
	v.mu.Lock()
	if c, ok := v.certs[certURL]; ok && now.Sub(c.at) < duerosCertCacheTTL {
		v.mu.Unlock()
		return c.cert, nil
	}
	v.mu.Unlock()

	fetch := v.FetchCert
	if fetch == nil {
		fetch = fetchDuerOSCert
	}
	pemBytes, err := fetch(ctx, certURL)
	if err != nil {
		return nil, fmt.Errorf("download signing certificate: %w", err)
	}
	cert, err := parseDuerOSCert(pemBytes)
	if err != nil {
		return nil, err
	}

	v.mu.Lock()
	if v.certs == nil {
		v.certs = map[string]cachedDuerOSCert{}
	}
	v.certs[certURL] = cachedDuerOSCert{cert: cert, at: now}
	v.mu.Unlock()
	return cert, nil
}

// validateDuerOSCertURL enforces the four URL rules from the spec. The host is
// pinned, so a forged header cannot turn the webhook into an SSRF proxy.
func validateDuerOSCertURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("signaturecerturl is not a valid URL: %w", err)
	}
	if !strings.EqualFold(u.Scheme, "https") {
		return fmt.Errorf("signaturecerturl must use https, got %q", u.Scheme)
	}
	if u.Hostname() != duerosCertHost {
		return fmt.Errorf("signaturecerturl host must be %s, got %q", duerosCertHost, u.Hostname())
	}
	port := u.Port()
	if port != "" && port != "443" {
		return fmt.Errorf("signaturecerturl port must be 443, got %q", port)
	}
	if !strings.HasPrefix(u.Path, duerosCertPathPfx) {
		return fmt.Errorf("signaturecerturl path must start with %s, got %q", duerosCertPathPfx, u.Path)
	}
	return nil
}

// parseDuerOSCert decodes the PEM certificate and pins its identity.
func parseDuerOSCert(pemBytes []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, fmt.Errorf("signing certificate is not PEM encoded")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse signing certificate: %w", err)
	}
	for _, name := range cert.DNSNames {
		if strings.EqualFold(name, duerosCertSAN) {
			return cert, nil
		}
	}
	return nil, fmt.Errorf("signing certificate SAN does not include %s (got %v)",
		duerosCertSAN, cert.DNSNames)
}

func fetchDuerOSCert(ctx context.Context, rawURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build cert request: %w", err)
	}
	req.Header.Set("User-Agent", "switch-monitor/1.0")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch cert: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("cert URL returned HTTP %d", resp.StatusCode)
	}
	pemBytes, err := io.ReadAll(io.LimitReader(resp.Body, duerosCertFetchSize))
	if err != nil {
		return nil, fmt.Errorf("read cert body: %w", err)
	}
	return pemBytes, nil
}

// ParseDuerOSRequest decodes a webhook body.
func ParseDuerOSRequest(body []byte) (*DuerOSRequest, error) {
	if len(strings.TrimSpace(string(body))) == 0 {
		return nil, fmt.Errorf("empty request body")
	}
	var req DuerOSRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, fmt.Errorf("decode dueros request: %w", err)
	}
	if strings.TrimSpace(req.Request.Type) == "" {
		return nil, fmt.Errorf("request.type is missing")
	}
	return &req, nil
}

// LogDuerOSRequest writes a privacy-conscious summary: the utterance is logged,
// but tokens and user ids are not.
func LogDuerOSRequest(req *DuerOSRequest, text string) {
	slog.Info("DuerOS skill request",
		"type", req.Request.Type,
		"session", req.Session.SessionID,
		"new_session", req.Session.New,
		"skill", req.ApplicationID(),
		"device", req.DeviceID(),
		"text", text)
}
