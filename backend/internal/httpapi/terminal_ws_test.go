package httpapi

import (
	"testing"

	domainterminal "github.com/lyming99/autoplan/backend/internal/domain/terminal"
)

func TestTerminalWSDecodeCommandRejectsDuplicateAndUnknownFields(t *testing.T) {
	for _, payload := range [][]byte{
		[]byte(`{"type":"input","type":"input","data":"ls\n"}`),
		[]byte(`{"type":"input","data":"ls\n","unexpected":true}`),
		[]byte(`{"type":"unknown"}`),
	} {
		if _, err := terminalWSDecodeCommand(payload); err == nil || err.closeCode != terminalWSCloseProtocol {
			t.Fatalf("payload %q accepted or received unexpected close code", payload)
		}
	}
}

func TestTerminalWSDecodeCommandAcceptsOnlyFrozenClientShapes(t *testing.T) {
	input, err := terminalWSDecodeCommand([]byte(`{"type":"input","data":"echo safe\n"}`))
	if err != nil || input.kind != "input" || input.data != "echo safe\n" {
		t.Fatal("input command was not decoded")
	}
	resize, err := terminalWSDecodeCommand([]byte(`{"type":"resize","cols":120,"rows":42}`))
	if err != nil || resize.kind != "resize" || resize.cols != 120 || resize.rows != 42 {
		t.Fatal("resize command was not decoded")
	}
	if _, err := terminalWSDecodeCommand([]byte(`{"type":"resize","cols":1,"rows":42}`)); err == nil || err.closeCode != terminalWSCloseInvalidData {
		t.Fatal("out-of-range resize was accepted")
	}
}

func TestTerminalWSOutboundKeepsTerminalEventsOffGenericChannels(t *testing.T) {
	outbound := newTerminalWSOutbound(1, 128)
	event := domainterminal.Event{Type: domainterminal.EventOutput, Output: &domainterminal.Output{Seq: 1, Data: "ok"}}
	if !outbound.enqueueEvent(event) {
		t.Fatal("bounded terminal event was not queued")
	}
	if outbound.enqueueEvent(event) {
		t.Fatal("full terminal send queue accepted another event")
	}
	outbound.stop(terminalWSCloseSlowConsumer, "slow consumer")
	if outbound.enqueueEvent(event) {
		t.Fatal("stopped terminal send queue accepted an event")
	}
}

func TestTerminalWSValidClosePayload(t *testing.T) {
	if !terminalWSValidClosePayload(nil) {
		t.Fatal("empty close payload should be valid")
	}
	if terminalWSValidClosePayload([]byte{0}) {
		t.Fatal("one-byte close payload should be invalid")
	}
	if terminalWSValidClosePayload([]byte{0x03, 0xed}) { // 1005 is reserved.
		t.Fatal("reserved close code should be invalid")
	}
}
