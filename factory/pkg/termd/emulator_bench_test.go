package termd

import (
	"fmt"
	"strings"
	"testing"
)

// benchStreams are output a session's emulator is fed: a coloured build
// log, and a TUI repainting its screen in place (cursor moves, erases,
// styled spans), as an engine's CLI does while it streams.
func benchStreams() map[string][]byte {
	var log strings.Builder
	for i := 0; log.Len() < 4<<20; i++ {
		fmt.Fprintf(&log, "\x1b[32mok\x1b[0m  \tgithub.com/example/pkg/sub%d\t%d.%03ds\t\x1b[2mcoverage: %d.%d%% of statements\x1b[0m\r\n", i%97, i%9, i%1000, i%100, i%10)
	}
	var tui strings.Builder
	for f := 0; tui.Len() < 4<<20; f++ {
		tui.WriteString("\x1b[?2026h\x1b[H")
		for r := 0; r < 30; r++ {
			fmt.Fprintf(&tui, "\x1b[%d;1H\x1b[2K\x1b[38;5;%dm│\x1b[0m \x1b[1m%s\x1b[0m %s", r+1, 33+r%6, strings.Repeat("▌", r%7), strings.Repeat("token ", 10+(f+r)%8))
		}
		tui.WriteString("\x1b[?2026l")
	}
	return map[string][]byte{"log": []byte(log.String()), "tui": []byte(tui.String())}
}

func BenchmarkEmulatorWrite(b *testing.B) {
	for name, data := range benchStreams() {
		b.Run(name, func(b *testing.B) {
			e, err := newEmulator(120, 40, func([]byte) {})
			if err != nil {
				b.Fatal(err)
			}
			defer e.Close()
			b.SetBytes(int64(len(data)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				for off := 0; off < len(data); off += 32 << 10 {
					e.Write(data[off:min(off+32<<10, len(data))])
				}
			}
		})
	}
}

func BenchmarkEmulatorSnapshot(b *testing.B) {
	data := benchStreams()["log"]
	e, err := newEmulator(120, 40, func([]byte) {})
	if err != nil {
		b.Fatal(err)
	}
	defer e.Close()
	e.Write(data)
	b.ResetTimer()
	var n int
	for i := 0; i < b.N; i++ {
		n = len(e.Snapshot())
	}
	b.ReportMetric(float64(n), "snapshot-bytes")
}
