package peer

// P3-2c — the pairing handshake (operator RULING 2026-07-16, Option 1).
//
// A new node is NOT a member yet, so it cannot run the N5 mutual membership
// handshake. Instead it presents a root-signed invite credential and proves it
// holds the matching device private key; the inviting node validates the
// credential (via the OnPair callback → cert root-verify + single-use) and
// appends the device.add that admits it (append-on-arrival). Wire, per line
// (newline-delimited JSON, same framing as the sync handshake):
//
//	dialer → server:  hello{mode:"pair", cairn_id}
//	dialer → server:  pairMsg{payload}            (opaque: {cert, invite_id})
//	server → dialer:  pairMsg{nonce}              (or {ok:false, err} to refuse)
//	dialer → server:  pairMsg{sig}                (sign(devicePriv, transcript))
//	server → dialer:  pairMsg{ok, event_id}       (or {ok:false, err})
//
// The invite payload deliberately carries NO private key — the new node keeps
// its key and answers the challenge locally, so the credential's secret half
// never crosses the wire to the inviting node.
//
// NOTE (hardening, deferred): the pairing handshake authenticates the DIALER to
// the server (key-possession) but not the server to the dialer. A rogue
// endpoint cannot forge real mesh membership (it is not root/member), so the
// worst it can do is refuse or falsely ack — detectable when the new node later
// fails to sync. Mutual pairing auth (server signs a chain-admitted nonce the
// dialer verifies against the invite's root) is a later hardening step.

import (
	"bufio"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/ggoosen/cairn/internal/config"
)

type pairMsg struct {
	Payload json.RawMessage `json:"payload,omitempty"`  // dialer→server: opaque invite ({cert, invite_id})
	Nonce   string          `json:"nonce,omitempty"`    // server→dialer: key-possession challenge
	Sig     string          `json:"sig,omitempty"`      // dialer→server: base64 sig over the transcript
	OK      bool            `json:"ok,omitempty"`       // server→dialer: verdict
	EventID string          `json:"event_id,omitempty"` // server→dialer: the appended device.add id
	Err     string          `json:"err,omitempty"`      // server→dialer: refusal reason
}

// pairTranscript binds the challenge signature to the mesh and nonce under a
// dedicated domain separator (never sign raw peer-supplied bytes).
func pairTranscript(cairnID, nonce string) []byte {
	return []byte(config.PairingHelloDomain + "|" + cairnID + "|" + nonce)
}

func writePair(w io.Writer, m pairMsg) error {
	blob, err := json.Marshal(m)
	if err != nil {
		return err
	}
	_, err = w.Write(append(blob, '\n'))
	return err
}

func readPair(r *bufio.Reader) (*pairMsg, error) {
	line, err := r.ReadBytes('\n')
	if err != nil {
		return nil, err
	}
	var m pairMsg
	if err := json.Unmarshal(line, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// handlePair is the server side of a pairing connection (dialer already sent the
// mode:"pair" hello, passed as their).
func (s *Server) handlePair(conn net.Conn, r *bufio.Reader, their *hello) {
	// 1. read the invite payload the dialer sent right after its hello, BEFORE
	// any refusal — the dialer is mid-write, so refusing first would deadlock a
	// synchronous transport (and leaves the message undrained on a buffered one).
	req, err := readPair(r)
	if err != nil {
		return
	}
	if s.OnPair == nil {
		_ = writePair(conn, pairMsg{OK: false, Err: "pairing not enabled on this node"})
		return
	}
	if their.CairnID != s.ident.CairnID {
		fmt.Fprintf(s.warn, "PAIR REFUSED: %s claims cairn %q (ours is %q)\n", conn.RemoteAddr(), their.CairnID, s.ident.CairnID)
		_ = writePair(conn, pairMsg{OK: false, Err: "wrong cairn"})
		return
	}
	devicePub, admit, err := s.OnPair(req.Payload)
	if err != nil {
		fmt.Fprintf(s.warn, "PAIR REFUSED: %s: %v\n", conn.RemoteAddr(), err)
		_ = writePair(conn, pairMsg{OK: false, Err: err.Error()})
		return
	}
	// 2. challenge for private-key possession
	nonce, err := newNonce()
	if err != nil {
		return
	}
	if err := writePair(conn, pairMsg{Nonce: nonce}); err != nil {
		return
	}
	// 3. verify the dialer's signature over the transcript
	ans, err := readPair(r)
	if err != nil {
		fmt.Fprintf(s.warn, "PAIR REFUSED: %s: connection dropped before proof\n", conn.RemoteAddr())
		return
	}
	sig, err := base64.StdEncoding.DecodeString(ans.Sig)
	if err != nil || !ed25519.Verify(devicePub, pairTranscript(s.ident.CairnID, nonce), sig) {
		fmt.Fprintf(s.warn, "PAIR REFUSED: %s failed the key-possession challenge\n", conn.RemoteAddr())
		_ = writePair(conn, pairMsg{OK: false, Err: "key-possession challenge failed"})
		return
	}
	// 4. admit — append the device.add (append-on-arrival, hard single-use)
	eventID, err := admit()
	if err != nil {
		fmt.Fprintf(s.warn, "PAIR REFUSED: %s: admission failed: %v\n", conn.RemoteAddr(), err)
		_ = writePair(conn, pairMsg{OK: false, Err: err.Error()})
		return
	}
	fmt.Fprintf(s.warn, "pair: admitted a new device via invitation (device.add %s)\n", eventID)
	_ = writePair(conn, pairMsg{OK: true, EventID: eventID})
}

// PairDial runs the dialer side of the pairing handshake over DefaultTransport:
// it presents the opaque invite payload, answers the key-possession challenge
// with devicePriv, and returns the appended device.add event id on success.
func PairDial(addr, cairnID string, payload []byte, devicePriv ed25519.PrivateKey) (string, error) {
	return PairDialWithTransport(DefaultTransport, addr, cairnID, payload, devicePriv)
}

// PairDialWithTransport is PairDial over an explicit transport (P3-1 seam).
func PairDialWithTransport(tr Transport, addr, cairnID string, payload []byte, devicePriv ed25519.PrivateKey) (string, error) {
	conn, err := tr.Dial(addr, config.SyncHelloTimeout)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(config.SyncHelloTimeout))
	r := bufio.NewReader(conn)

	// 1. announce pairing mode
	if err := writeMsg(conn, hello{V: 1, CairnID: cairnID, Mode: "pair"}); err != nil {
		return "", err
	}
	// 2. present the invite credential
	if err := writePair(conn, pairMsg{Payload: payload}); err != nil {
		return "", err
	}
	// 3. the challenge (or a refusal)
	ch, err := readPair(r)
	if err != nil {
		return "", fmt.Errorf("peer closed during pairing (is pairing enabled? invite valid?): %w", err)
	}
	if ch.Nonce == "" {
		return "", fmt.Errorf("pairing refused: %s", refusalReason(ch))
	}
	// 4. prove possession of the device key
	sig := ed25519.Sign(devicePriv, pairTranscript(cairnID, ch.Nonce))
	if err := writePair(conn, pairMsg{Sig: base64.StdEncoding.EncodeToString(sig)}); err != nil {
		return "", err
	}
	// 5. verdict
	verdict, err := readPair(r)
	if err != nil {
		return "", fmt.Errorf("peer dropped after our proof (refused?): %w", err)
	}
	if !verdict.OK {
		return "", fmt.Errorf("pairing refused: %s", refusalReason(verdict))
	}
	return verdict.EventID, nil
}

func refusalReason(m *pairMsg) string {
	if m.Err != "" {
		return m.Err
	}
	return "no reason given"
}
