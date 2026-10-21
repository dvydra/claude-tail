package main

import (
	"strings"
	"testing"
)

func TestKeyActionForCtrlT(t *testing.T) {
	if got := liveKey([]byte{0x14}); got != keySend {
		t.Errorf("ctrl-t = %v, want keySend", got)
	}
}

// Ctrl-T ends send mode wherever it lands in a read; nothing after it is sent.
func TestSplitAtToggle(t *testing.T) {
	for _, c := range []struct {
		in, want string
		toggled  bool
	}{
		{"abc", "abc", false},
		{"ab\x14cd", "ab", true},
		{"\x14", "", true},
		{"", "", false},
	} {
		got, toggled := splitAtToggle([]byte(c.in))
		if string(got) != c.want || toggled != c.toggled {
			t.Errorf("splitAtToggle(%q) = %q, %v", c.in, got, toggled)
		}
	}
}

// A cbreak tty turns Enter into \n (ICRNL); the agent submits on \r.
func TestSendBytesEnterIsCR(t *testing.T) {
	if got := sendBytes([]byte("hi\n")); string(got) != "hi\r" {
		t.Errorf("sendBytes = %q", got)
	}
}

// A rune split across two reads must not be sent as two broken halves.
func TestCompleteUTF8(t *testing.T) {
	e := []byte("é") // 2 bytes
	send, rest := completeUTF8(append([]byte("a"), e[0]))
	if string(send) != "a" || len(rest) != 1 {
		t.Errorf("split rune: send %q rest %q", send, rest)
	}
	send, rest = completeUTF8([]byte("aé"))
	if string(send) != "aé" || len(rest) != 0 {
		t.Errorf("whole: send %q rest %q", send, rest)
	}
}

func TestSendKeysScript(t *testing.T) {
	s := sendKeysScript("PANE-1", []byte("say \"hi\"\x1b[A\x7f\r"))
	for _, want := range []string{
		`if (id of s) is "PANE-1" then`,
		`tell s to write text ("say \"hi\"" & (character id 27) & "[A" & (character id 127) & (character id 13)) newline NO`,
		"try",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("script missing %q:\n%s", want, s)
		}
	}
}

// Every key queued before stop lands, in order, and a rune split across two
// sends arrives whole.
func TestSenderDeliversInOrder(t *testing.T) {
	var scripts []string
	prev := sendRun
	sendRun = func(s string) error { scripts = append(scripts, s); return nil }
	t.Cleanup(func() { sendRun = prev })

	e := []byte("é")
	send, stop := startSender("P")
	send([]byte("ab"))
	send([]byte{'c', e[0]})
	send([]byte{e[1], '!'})
	stop()

	joined := strings.Join(scripts, "")
	var got strings.Builder
	for _, line := range strings.Split(joined, "\n") {
		if i := strings.Index(line, "write text ("); i >= 0 {
			got.WriteString(line[i:])
		}
	}
	for _, part := range []string{`"ab`, `é!"`} {
		if !strings.Contains(got.String(), part) {
			t.Errorf("sent %q, missing %q", got.String(), part)
		}
	}
	if strings.Index(got.String(), "ab") > strings.Index(got.String(), "é!") {
		t.Errorf("out of order: %q", got.String())
	}
}

func TestASKeysExpr(t *testing.T) {
	if got := asKeysExpr([]byte("x")); got != `("x")` {
		t.Errorf("plain = %s", got)
	}
	if got := asKeysExpr([]byte{0x1b}); got != `((character id 27))` {
		t.Errorf("control = %s", got)
	}
}
