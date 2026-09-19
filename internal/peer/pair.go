package peer

// P3-2c — the pairing handshake (operator RULING 2026-07-16, Option 1),
// P3-5 — made MUTUAL (S9).
//
// A new node is NOT a member yet, so it cannot run the N5 mutual membership
// handshake. Instead it presents a root-signed invite credential and proves it
// holds the matching device private key; the inviting node validates the
// credential (via the OnPair callback → cert root-verify + single-use) and
// appends the device.add that admits it (append-on-arrival).
//
// P3-5 closes the direction that was missing: the inviting node now proves
// ITSELF to the dialer FIRST, against the mesh chain the invitation carries.
// The dialer verified that chain from genesis (identity.VerifyPairingInvitation),
// so it can apply the same R27 rule the N5 handshake applies — member, not
// revoked, signature over a nonce-bound transcript — before it hands over its
// credential. Wire, per line (newline-delimited JSON, same framing as the sync
// handshake):
//
//	dialer → server:  hello{mode:"pair", cairn_id, nonce}        (nonce = the dialer's challenge)
//	server → dialer:  pairMsg{v, server_device, nonce, sig}      (or {ok:false, err} to refuse)
//	dialer → server:  pairMsg{payload, sig}                      (payload opaque: {cert, invite_id})
//	server → dialer:  pairMsg{ok, event_id}                      (or {ok:false, err})
//
// Two properties follow, and both are the point:
//
//   - the credential is NEVER handed to an unauthenticated endpoint — the
//     dialer aborts before writing the payload if the server fails R27, so a
//     rogue listener on the pairing port learns nothing at all;
//   - a false ack is no longer possible from a non-member: the "ok" arrives on
//     a connection whose far end proved a mesh device key.
//
// A faithful relay between two honest nodes still succeeds — it forwards both
// proofs unmodified and the real node admits the real device, which is the
// intended outcome; it cannot substitute a device, forge an admission, or read
// a key. That is the same residual as the N5 handshake.
//
// The invite payload deliberately carries NO private key — the new node keeps
// its key and answers the challenge locally, so the credential's secret half
// never crosses the wire to the inviting node.
//
// Both proofs are bound to BOTH nonces and to the server's device id under
// domain separators distinct from the sync handshake's, so no signature from
// one protocol, direction, or session is usable in another.

import (
	"bufio"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/ggoosen/cairn/internal/config"
)

type pairMsg struct {
	V            int             `json:"v,omitempty"`             // server→dialer: pairing wire version (P3-5)
	ServerDevice string          `json:"server_device,omitempty"` // server→dialer: the device id it proves
	Payload      json.RawMessage `json:"payload,omitempty"`       // dialer→server: opaque invite ({cert, invite_id})
	Nonce        string          `json:"nonce,omitempty"`         // server→dialer: key-possession challenge
	Sig          string          `json:"sig,omitempty"`           // base64 sig over the relevant transcript
	OK           bool            `json:"ok,omitempty"`            // server→dialer: verdict
	EventID      string          `json:"event_id,omitempty"`      // server→dialer: the appended device.add id
	Err          string          `json:"err,omitempty"`           // server→dialer: refusal reason
}

// pairTranscript binds the DIALER's key-possession signature to the mesh, the
// server device it authenticated, and both nonces, under a dedicated domain
// separator (never sign raw peer-supplied bytes).
func pairTranscript(cairnID, serverDevice, nonceServer, nonceDialer string) []byte {
	return []byte(config.PairingHelloDomain + "|" + cairnID + "|" + serverDevice + "|" + nonceServer + "|" + nonceDialer)
}

// pairServerTranscript binds the SERVER's proof to the mesh, its own device id,
// and both nonces, under a separator distinct from both the dialer's pairing
// transcript and the N5 sync transcript.
func pairServerTranscript(cairnID, serverDevice, nonceServer, nonceDialer string) []byte {
	return []byte(config.PairingServerHelloDomain + "|" + cairnID + "|" + serverDevice + "|" + nonceServer + "|" + nonceDialer)
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
	// 0. P3-5: a dialer that sent no nonce speaks the pre-mutual pairing wire —
	// it cannot authenticate us, so we refuse rather than silently downgrade.
	// Drain its next message first: that dialer writes its payload immediately
	// after the hello, so on a synchronous transport refusing without draining
	// would deadlock (and would leave the message undrained on a buffered one).
	if their.Nonce == "" {
		_, _ = readPair(r)
		fmt.Fprintf(s.warn, "PAIR REFUSED: %s speaks the pre-mutual pairing protocol\n", conn.RemoteAddr())
		_ = writePair(conn, pairMsg{OK: false, Err: pairLegacyDialerRefusal})
		return
	}
	// 1. refusals that need no dialer input come BEFORE we sign anything: never
	// act as a signing oracle for a dialer we are not going to serve. The dialer
	// writes nothing until it has read this message, so no deadlock is possible.
	if s.OnPair == nil {
		_ = writePair(conn, pairMsg{OK: false, Err: "pairing not enabled on this node"})
		return
	}
	if their.CairnID != s.ident.CairnID {
		fmt.Fprintf(s.warn, "PAIR REFUSED: %s claims cairn %q (ours is %q)\n", conn.RemoteAddr(), their.CairnID, s.ident.CairnID)
		_ = writePair(conn, pairMsg{OK: false, Err: "wrong cairn"})
		return
	}
	// 2. prove OURSELVES to the dialer, over both nonces (P3-5). The dialer
	// checks this against the invitation's genesis-verified chain and hands over
	// its credential only if it passes.
	nonceMine, err := newNonce()
	if err != nil {
		return
	}
	sig := ed25519.Sign(s.ident.Priv, pairServerTranscript(s.ident.CairnID, s.ident.DeviceID, nonceMine, their.Nonce))
	if err := writePair(conn, pairMsg{
		V: config.PairProtocolVersion, ServerDevice: s.ident.DeviceID, Nonce: nonceMine,
		Sig: base64.StdEncoding.EncodeToString(sig),
	}); err != nil {
		return
	}
	// 3. the dialer's credential + its proof of key possession, in one message
	// (it already holds our challenge nonce, so no extra round trip is needed).
	req, err := readPair(r)
	if err != nil {
		fmt.Fprintf(s.warn, "PAIR REFUSED: %s: connection dropped before the credential (did it refuse US?)\n", conn.RemoteAddr())
		return
	}
	devicePub, admit, err := s.OnPair(req.Payload)
	if err != nil {
		fmt.Fprintf(s.warn, "PAIR REFUSED: %s: %v\n", conn.RemoteAddr(), err)
		_ = writePair(conn, pairMsg{OK: false, Err: err.Error()})
		return
	}
	dialerSig, err := base64.StdEncoding.DecodeString(req.Sig)
	if err != nil || !ed25519.Verify(devicePub, pairTranscript(s.ident.CairnID, s.ident.DeviceID, nonceMine, their.Nonce), dialerSig) {
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

// pairLegacyDialerRefusal is what a pre-P3-5 dialer is told. It names the fix
// rather than the symptom: that dialer cannot authenticate the node it is about
// to hand a credential to.
const pairLegacyDialerRefusal = "this node requires MUTUAL pairing authentication (pairing protocol v2): the joining node must also verify us against the invitation's chain — upgrade cairn on the joining node"

// PairDial runs the dialer side of the pairing handshake over DefaultTransport:
// it authenticates the inviting node against trust, presents the opaque invite
// payload, answers the key-possession challenge with devicePriv, and returns the
// appended device.add event id on success.
//
// trust is the mesh membership the JOINING node verified from genesis — the
// invitation's chain (identity.VerifyPairingInvitation) or the installed pairing
// bootstrap (identity.PairingBootstrapTrust). It is mandatory: without it the
// dialer cannot tell a mesh node from a rogue listener, so a nil trust is
// refused rather than treated as "skip the check".
func PairDial(addr, cairnID string, payload []byte, devicePriv ed25519.PrivateKey, trust Trust) (string, error) {
	return PairDialWithTransport(DefaultTransport, addr, cairnID, payload, devicePriv, trust)
}

// PairDialWithTransport is PairDial over an explicit transport (P3-1 seam).
func PairDialWithTransport(tr Transport, addr, cairnID string, payload []byte, devicePriv ed25519.PrivateKey, trust Trust) (string, error) {
	if trust == nil {
		return "", errors.New("pairing requires the invitation's verified mesh trust (the inviting node must authenticate to us too)")
	}
	conn, err := tr.Dial(addr, config.SyncHelloTimeout)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(config.SyncHelloTimeout))
	r := bufio.NewReader(conn)

	nonceMine, err := newNonce()
	if err != nil {
		return "", err
	}
	// 1. announce pairing mode and challenge the node to prove itself
	if err := writeMsg(conn, hello{V: 1, CairnID: cairnID, Mode: "pair", Nonce: nonceMine}); err != nil {
		return "", err
	}
	// 2. its proof (or a refusal). NOTHING of ours has been sent yet beyond the
	// nonce, so a failure here leaks no credential.
	ch, err := readPair(r)
	if err != nil {
		return "", fmt.Errorf("peer closed during pairing (is pairing enabled? does it run a cairn with mutual pairing authentication?): %w", err)
	}
	if ch.Nonce == "" || ch.Sig == "" {
		return "", fmt.Errorf("pairing refused: %s", refusalReason(ch))
	}
	if ch.V != config.PairProtocolVersion {
		return "", fmt.Errorf("the inviting node speaks pairing protocol v%d, we speak v%d — upgrade cairn on the inviting node", ch.V, config.PairProtocolVersion)
	}
	// 3. R27 on the inviting node, using the chain the invitation carried: same
	// rule, same code path, as the N5 membership handshake applies to a peer.
	// verifyPeer reads the presented identity out of a hello; the pairing wire
	// carries the same three fields under different names, so adapt rather than
	// duplicate the rule. The cairn binding is not weakened by filling it in
	// here: it is inside the SIGNED transcript, and trust is this mesh's chain,
	// so a foreign-mesh device cannot produce an accepted signature.
	check := &hello{V: 1, CairnID: cairnID, DeviceID: ch.ServerDevice, Sig: ch.Sig}
	if err := verifyPeer(trust, cairnID, check,
		pairServerTranscript(cairnID, ch.ServerDevice, ch.Nonce, nonceMine)); err != nil {
		return "", fmt.Errorf("the node at %s failed authentication against the invitation's chain — NOT handing it the invitation: %w", addr, err)
	}
	// 4. only now: the credential, plus proof we hold the device key
	sig := ed25519.Sign(devicePriv, pairTranscript(cairnID, ch.ServerDevice, ch.Nonce, nonceMine))
	if err := writePair(conn, pairMsg{Payload: payload, Sig: base64.StdEncoding.EncodeToString(sig)}); err != nil {
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
