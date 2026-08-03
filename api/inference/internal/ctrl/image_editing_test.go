package ctrl

import (
	"bytes"
	"strings"
	"testing"
)

// TestGetImageEditingInputFeeAndImageNum_JSON_ValidN covers the happy path: a
// JSON body with an "n" field and a valid integer.
func TestGetImageEditingInputFeeAndImageNum_JSON_ValidN(t *testing.T) {
	c := &Ctrl{}
	fee, n, err := c.GetImageEditingInputFeeAndImageNum(
		[]byte(`{"prompt":"x","n":3}`), "application/json",
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fee != "0" {
		t.Errorf("fee = %q, want 0", fee)
	}
	if n != 3 {
		t.Errorf("n = %d, want 3", n)
	}
}

// TestGetImageEditingInputFeeAndImageNum_JSON_MissingN defaults to 1.
func TestGetImageEditingInputFeeAndImageNum_JSON_MissingN(t *testing.T) {
	c := &Ctrl{}
	_, n, err := c.GetImageEditingInputFeeAndImageNum(
		[]byte(`{"prompt":"x"}`), "application/json",
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 1 {
		t.Errorf("n = %d, want default 1", n)
	}
}

// TestGetImageEditingInputFeeAndImageNum_Multipart_ValidN covers a well-formed
// multipart with n=5.
func TestGetImageEditingInputFeeAndImageNum_Multipart_ValidN(t *testing.T) {
	body := []byte(
		"--b\r\nContent-Disposition: form-data; name=\"prompt\"\r\n\r\nhi\r\n" +
			"--b\r\nContent-Disposition: form-data; name=\"n\"\r\n\r\n5\r\n" +
			"--b--\r\n",
	)
	c := &Ctrl{}
	_, n, err := c.GetImageEditingInputFeeAndImageNum(body, `multipart/form-data; boundary=b`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 5 {
		t.Errorf("n = %d, want 5", n)
	}
}

// TestGetImageEditingInputFeeAndImageNum_Multipart_AdversarialFileContent is
// the critical regression test: a file part whose BYTES contain the literal
// `name="n"\r\n\r\n999` must NOT be misread as the billing field. The old
// byte-scanner parser would have returned 999 here, overbilling the user
// (or underbilling the provider, depending on direction). mime/multipart.Reader
// respects MIME boundaries so the adversarial substring lives inside the file
// part and is ignored.
func TestGetImageEditingInputFeeAndImageNum_Multipart_AdversarialFileContent(t *testing.T) {
	// File body deliberately embeds name="n"\r\n\r\n999 — the old scanner's
	// pattern. "--XYZ" at the end is NOT the real boundary ("b") so a proper
	// parser keeps all of this inside the file part.
	adversarial := []byte(
		"PNG-HEADER" +
			"name=\"n\"\r\n\r\n999\r\n--XYZ " +
			"more-bytes",
	)
	var body []byte
	// File part comes FIRST so the old substring-scan finds the adversarial
	// "n" before the legitimate one.
	body = append(body, []byte("--b\r\nContent-Disposition: form-data; name=\"image\"; filename=\"x.bin\"\r\nContent-Type: application/octet-stream\r\n\r\n")...)
	body = append(body, adversarial...)
	body = append(body, []byte("\r\n--b\r\nContent-Disposition: form-data; name=\"prompt\"\r\n\r\nhi\r\n")...)
	body = append(body, []byte("--b\r\nContent-Disposition: form-data; name=\"n\"\r\n\r\n2\r\n")...)
	body = append(body, []byte("--b--\r\n")...)

	c := &Ctrl{}
	_, n, err := c.GetImageEditingInputFeeAndImageNum(body, `multipart/form-data; boundary=b`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n == 999 {
		t.Fatal("adversarial file bytes spoofed the n field — old byte-scanner bug regressed")
	}
	if n != 2 {
		t.Errorf("n = %d, want 2 (from the legitimate form field)", n)
	}
}

// TestGetImageEditingInputFeeAndImageNum_Multipart_MissingN defaults to 1.
func TestGetImageEditingInputFeeAndImageNum_Multipart_MissingN(t *testing.T) {
	body := []byte(
		"--b\r\nContent-Disposition: form-data; name=\"prompt\"\r\n\r\nhi\r\n" +
			"--b--\r\n",
	)
	c := &Ctrl{}
	_, n, err := c.GetImageEditingInputFeeAndImageNum(body, `multipart/form-data; boundary=b`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 1 {
		t.Errorf("n = %d, want default 1", n)
	}
}

// TestParseMultipartN_RejectsNonNumericAndOversized covers malicious inputs: a
// non-numeric value errors, and a 32-byte limit prevents a "name=n" file part
// from streaming arbitrary data into memory.
func TestParseMultipartN_RejectsNonNumericAndOversized(t *testing.T) {
	t.Run("non-numeric", func(t *testing.T) {
		body := []byte(
			"--b\r\nContent-Disposition: form-data; name=\"n\"\r\n\r\nnot-a-number\r\n" +
				"--b--\r\n",
		)
		if _, err := parseMultipartN(body, `multipart/form-data; boundary=b`); err == nil {
			t.Error("expected error for non-numeric n value")
		}
	})
	t.Run("oversized-does-not-oom", func(t *testing.T) {
		// An attacker could label a 1MB payload as name="n" and try to exhaust
		// broker memory during billing extraction. LimitReader caps the read at
		// 32 bytes — the important property is bounded memory, not parse success.
		// ParseInt may then reject (overflow) or accept; both outcomes are safe.
		big := bytes.Repeat([]byte("9"), 1<<20)
		var body []byte
		body = append(body, []byte("--b\r\nContent-Disposition: form-data; name=\"n\"\r\n\r\n")...)
		body = append(body, big...)
		body = append(body, []byte("\r\n--b--\r\n")...)
		// Just invoking without OOM / panic is the assertion. The error or
		// success value is incidental.
		_, _ = parseMultipartN(body, `multipart/form-data; boundary=b`)
	})
}

// TestGetImageEditingInputFeeAndImageNum_Multipart_DisagreementIsRefused pins the two shapes where
// this gate used to read a CHEAPER `n` than the upstream's form parser would. `n` multiplies the fee,
// so each was a measured discount: bill one image, render ten.
func TestGetImageEditingInputFeeAndImageNum_Multipart_DisagreementIsRefused(t *testing.T) {
	c := &Ctrl{logger: testLogger()}
	part := func(name, value string) string {
		return "--b\r\nContent-Disposition: form-data; name=\"" + name + "\"\r\n\r\n" + value + "\r\n"
	}

	t.Run("repeated n is refused, not read as the first value", func(t *testing.T) {
		body := []byte(part("n", "1") + part("n", "10") + "--b--\r\n")
		if _, n, err := c.GetImageEditingInputFeeAndImageNum(body, `multipart/form-data; boundary=b`); err == nil {
			t.Errorf("repeated n accepted as %d; Starlette/FastAPI take the LAST value, so this billed 1 and rendered 10", n)
		}
	})

	t.Run("n padded past the read cap is refused, not defaulted to 1", func(t *testing.T) {
		padded := strings.Repeat(" ", maxMultipartFieldBytes+1) + "10"
		body := []byte(part("n", padded) + "--b--\r\n")
		if _, n, err := c.GetImageEditingInputFeeAndImageNum(body, `multipart/form-data; boundary=b`); err == nil {
			t.Errorf("over-long n accepted as %d; the upstream trims the padding and reads 10", n)
		}
	})

	t.Run("a single well-formed n still works", func(t *testing.T) {
		body := []byte(part("n", "10") + "--b--\r\n")
		_, n, err := c.GetImageEditingInputFeeAndImageNum(body, `multipart/form-data; boundary=b`)
		if err != nil || n != 10 {
			t.Errorf("n = %d (err %v), want 10", n, err)
		}
	})
}
