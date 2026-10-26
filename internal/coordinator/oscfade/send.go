package oscfade

import (
	"encoding/binary"
	"fmt"
)

// Message is one OSC message carrying a single int32 argument.
type Message struct {
	Address string
	Int     int32
}

// IntMessage returns a message carrying one int32.
func IntMessage(address string, v int32) Message { return Message{Address: address, Int: v} }

// encode builds the message's OSC packet.
func (m Message) encode() []byte {
	buf := appendOSCString(nil, m.Address)
	buf = appendOSCString(buf, ",i")
	return binary.BigEndian.AppendUint32(buf, uint32(m.Int))
}

// Arena's positional OSC addresses. Every index argument is the
// composition file's 0-based index; Arena's OSC counts from 1.

func ClipConnectAddress(layerIndex, columnIndex int) string {
	return fmt.Sprintf("/composition/layers/%d/clips/%d/connect", layerIndex+1, columnIndex+1)
}

func DeckSelectAddress(deckIndex int) string {
	return fmt.Sprintf("/composition/decks/%d/select", deckIndex+1)
}

func ColumnConnectAddress(columnIndex int) string {
	return fmt.Sprintf("/composition/columns/%d/connect", columnIndex+1)
}

func LayerClearAddress(layerIndex int) string {
	return fmt.Sprintf("/composition/layers/%d/clear", layerIndex+1)
}

func LayerBypassedAddress(layerIndex int) string {
	return fmt.Sprintf("/composition/layers/%d/bypassed", layerIndex+1)
}

func GroupColumnConnectAddress(groupIndex, columnIndex int) string {
	return fmt.Sprintf("/composition/groups/%d/columns/%d/connect", groupIndex+1, columnIndex+1)
}

func GroupClearAddress(groupIndex int) string {
	return fmt.Sprintf("/composition/groups/%d/disconnectlayers", groupIndex+1)
}

// DisconnectAllAddress stops every clip in the composition.
const DisconnectAllAddress = "/composition/disconnectall"

// Send writes msgs in order on the fader's socket. A write error stops at
// the failing message; earlier messages were already sent.
func (f *Fader) Send(msgs ...Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.connectLocked(); err != nil {
		return err
	}
	for _, m := range msgs {
		if _, err := f.conn.Write(m.encode()); err != nil {
			return err
		}
	}
	return nil
}

// SetLevel cancels any running fade on layerIndex and sends its master v once.
func (f *Fader) SetLevel(layerIndex int, v float64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.connectLocked(); err != nil {
		return err
	}
	if cancel, ok := f.cancels[layerIndex]; ok {
		cancel()
		delete(f.cancels, layerIndex)
		f.gen[layerIndex]++
	}
	if _, err := f.conn.Write(encodeOSCFloat(LayerMasterAddress(layerIndex), float32(v))); err != nil {
		return err
	}
	f.lastSent[layerIndex] = v
	return nil
}

// LastSent returns the last master level sent to layerIndex, if any.
func (f *Fader) LastSent(layerIndex int) (float64, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.lastSent[layerIndex]
	return v, ok
}
