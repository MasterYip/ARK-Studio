package org

import "testing"

// Tokens sealed by CPython's org.SecretBox with
// ARK_ORG_SECRET_KEY=00112233...eeff (twice).
func TestOpenSecretAgainstCPython(t *testing.T) {
	t.Setenv("ARK_ORG_SECRET_KEY", "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff")
	secretKeys.Range(func(k, _ any) bool { secretKeys.Delete(k); return true })
	cases := []struct{ token, aad, want string }{
		{"ark1$n9vCwPeofGjLsz4xw_G5TQ$JTxQTraXCBLK9EMCYuEQE5Nj_go$WXlSUERWlhafT71QiXnt6gwxJVzlIbO7mX97PcJ44t4",
			"u1:assistant:api_key", "sk-tést-1234 中文"},
		{"ark1$NL6RquuxPH5UXns9DtdboA$$LoBg7rqHZmVKeFM40einrlTTVCR4EZ_TV7D1N1k5XUw",
			"u2:agents:env:codex:OPENAI_API_KEY", ""},
		{"ark1$rB5aH5gg4DUJ7CkRoLxPgw$Us-Z8arwmCnwOBUbH15cyKxc8puWMpELuSkWf4z_v1ymKnA1GfHR93HkWEHsA8eBK_zIj7c9-svWP4vpWT1ihbNjAlTUiQ$2yBWv5HyhrLGwyC1e8yLhIGwe_e2gL89Iy_Gf62py7o",
			"id:assistant:k", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"},
	}
	for _, c := range cases {
		got, err := OpenSecret(c.token, c.aad)
		if err != nil || got != c.want {
			t.Errorf("OpenSecret(%q) = %q, %v; want %q", c.aad, got, err, c.want)
		}
	}
	if _, err := OpenSecret(cases[0].token, "u9:assistant:api_key"); err == nil || err.(*Error).Code != "SECRET_KEY_MISMATCH" {
		t.Errorf("wrong aad: %v", err)
	}
	for token, code := range map[string]string{"nope": "SECRET_CORRUPT", "ark2$a$b$c": "SECRET_CORRUPT", "ark1$!!$b$c": "SECRET_CORRUPT"} {
		if _, err := OpenSecret(token, "x"); err == nil || err.(*Error).Code != code {
			t.Errorf("%q: %v", token, err)
		}
	}
	secretKeys.Range(func(k, _ any) bool { secretKeys.Delete(k); return true })
}
