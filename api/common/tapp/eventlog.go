package tapp

import (
	"crypto/sha512"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// Event is one TCG2 entry of a CC event log, reduced to what verification
// reads: the measurement register it extended, the event type, its SHA-384
// digest (nil when the entry carries none) and the event data.
type Event struct {
	Index  uint32 // CC log index: 0 = MRTD (not replayable), 1..4 = RTMR0..3
	Type   uint32
	SHA384 []byte
	Data   []byte
}

const (
	evEFIBootServicesApplication = 0x80000003
	evIPL                        = 0xd
	rtmrLen                      = 48
)

func digestLen(alg uint16) int {
	switch alg {
	case 0x4:
		return 20
	case 0xb:
		return 32
	case 0xc:
		return 48
	case 0xd:
		return 64
	}
	return 48
}

// ParseCCEventLog decodes a TCG2 event log as a TDX guest exposes it
// (ACPI CCEL): a SHA1-format spec-ID header entry, then TCG_PCR_EVENT2
// entries. Truncated trailing bytes end the log rather than failing it,
// which is also how 0g-tapp's own reader behaves.
func ParseCCEventLog(log []byte) ([]Event, error) {
	o := 8 + 20 // header entry: index, type, sha1 digest
	if o+4 > len(log) {
		return nil, errors.New("event log shorter than its header entry")
	}
	ds := int(binary.LittleEndian.Uint32(log[o:]))
	o += 4 + ds
	var out []Event
	for o+12 <= len(log) {
		idx := binary.LittleEndian.Uint32(log[o:])
		typ := binary.LittleEndian.Uint32(log[o+4:])
		cnt := int(binary.LittleEndian.Uint32(log[o+8:]))
		o += 12
		var sha384 []byte
		for i := 0; i < cnt; i++ {
			if o+2 > len(log) {
				return out, nil
			}
			alg := binary.LittleEndian.Uint16(log[o:])
			o += 2
			n := digestLen(alg)
			if o+n > len(log) {
				return out, nil
			}
			if alg == 0xc {
				sha384 = append([]byte(nil), log[o:o+n]...)
			}
			o += n
		}
		if o+4 > len(log) {
			break
		}
		dl := int(binary.LittleEndian.Uint32(log[o:]))
		o += 4
		if o+dl > len(log) {
			break
		}
		out = append(out, Event{Index: idx, Type: typ, SHA384: sha384, Data: log[o : o+dl]})
		o += dl
	}
	return out, nil
}

// ReplayRTMRs folds every SHA-384 entry into its register: starting from 48
// zero bytes, rtmr = sha384(rtmr ‖ digest), in log order. Index i of the CC
// log is RTMR i-1; index 0 (MRTD) is not a replayable register and is skipped.
// Measured against live logs on 2026-10-21: indexes 1, 2 and 4 reproduce
// RTMR0, RTMR1 and RTMR3 of the quote; RTMR2 stays zero in these images.
func ReplayRTMRs(events []Event) [4][rtmrLen]byte {
	var mr [4][rtmrLen]byte
	for _, e := range events {
		if e.Index < 1 || e.Index > 4 || len(e.SHA384) != rtmrLen {
			continue
		}
		r := &mr[e.Index-1]
		h := sha512.New384()
		h.Write(r[:])
		h.Write(e.SHA384)
		copy(r[:], h.Sum(nil))
	}
	return mr
}

func eventText(data []byte) string {
	var b strings.Builder
	for _, c := range data {
		if c != 0 {
			b.WriteByte(c)
		}
	}
	return strings.ToLower(b.String())
}

// BootComponents names the boot-chain measurements the way 0g-tapp's
// reference-value files do ("measurement.<component>.SHA-384"): the UKI is
// the EFI boot application bootx64.efi; shim/grub likewise; kernel, initrd
// and the kernel command line are IPL events. Digests are lowercase hex.
func BootComponents(events []Event) map[string][]string {
	out := map[string][]string{}
	for _, e := range events {
		if e.SHA384 == nil {
			continue
		}
		text := eventText(e.Data)
		var c string
		switch e.Type {
		case evEFIBootServicesApplication:
			switch {
			case strings.Contains(text, "shimx64.efi"):
				c = "shim"
			case strings.Contains(text, "grubx64.efi"):
				c = "grub"
			case strings.Contains(text, "bootx64.efi"):
				c = "uki"
			}
		case evIPL:
			switch {
			case strings.HasPrefix(text, "kernel_cmdline:"):
				c = "kernel_cmdline"
			case strings.Contains(text, "vmlinuz"):
				c = "kernel"
			case strings.Contains(text, "initrd"):
				c = "initrd"
			}
		}
		if c != "" {
			out[c] = append(out[c], hex.EncodeToString(e.SHA384))
		}
	}
	return out
}

func hexEq(a, b string) bool {
	a = strings.ToLower(strings.TrimPrefix(a, "0x"))
	b = strings.ToLower(strings.TrimPrefix(b, "0x"))
	return a != "" && a == b
}

func rtmrString(r [rtmrLen]byte) string { return hex.EncodeToString(r[:]) }

func fmtMismatch(i int, want, got [rtmrLen]byte) string {
	return fmt.Sprintf("RTMR%d quote %s… replay %s…", i, rtmrString(want)[:16], rtmrString(got)[:16])
}
