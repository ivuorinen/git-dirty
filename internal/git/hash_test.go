package git

import (
	"strings"
	"testing"
)

func TestHashObject_MatchesGit(t *testing.T) {
	bodies := []string{"", "a\n", strings.Repeat("blob content\n", 1000), "\x00binary\xff"}
	for _, f := range formats {
		t.Run(f.Name, func(t *testing.T) {
			dir := newRepo(t, f)
			if got := run(t, dir, "rev-parse", "--show-object-format"); got != f.Name {
				t.Fatalf("fixture object format = %s", got)
			}
			for i, body := range bodies {
				want, _ := runCode(t, dir, []byte(body), "hash-object", "--stdin")
				got := f.HashObject(ObjBlob, []byte(body))
				if got.String() != want {
					t.Errorf("body %d: id = %s, git hash-object = %s", i, got, want)
				}
				parsed, err := f.ParseHex(want)
				if err != nil || parsed != got {
					t.Errorf("body %d: ParseHex(%s) = %v, %v", i, want, parsed, err)
				}
			}
		})
	}
}

func TestObjectID_Zero(t *testing.T) {
	var zero ObjectID
	if !zero.IsZero() || zero.String() != "" {
		t.Errorf("zero value: IsZero=%v String=%q", zero.IsZero(), zero.String())
	}
	allZero, err := SHA1.ParseHex(strings.Repeat("0", 40))
	if err != nil || !allZero.IsZero() {
		t.Errorf("all-zero id: %v %v", allZero, err)
	}
}

func TestObjectID_Comparable(t *testing.T) {
	a, _ := SHA1.ParseHex(strings.Repeat("ab", 20))
	b, _ := SHA256.ParseHex(strings.Repeat("ab", 32))
	if a == b {
		t.Error("ids of different lengths compare equal")
	}
	m := map[ObjectID]bool{a: true}
	if !m[a] || m[b] {
		t.Error("map lookup by id is wrong")
	}
}

func TestObjectID_Malformed(t *testing.T) {
	for _, bad := range []string{"", "abc", strings.Repeat("zz", 20), strings.Repeat("ab", 21)} {
		if _, err := SHA1.ParseHex(bad); err == nil {
			t.Errorf("ParseHex(%q): want error", bad)
		}
	}
	if _, err := SHA1.IDFromBytes(make([]byte, 32)); err == nil {
		t.Error("IDFromBytes with 32 bytes for SHA-1: want error")
	}
}

func TestCheckFormat(t *testing.T) {
	const objectFormat = "objectformat"
	tests := []struct {
		name    string
		version string
		ext     map[string]string
		want    Format
		wantErr string
	}{
		{name: "default", want: SHA1},
		{name: "v0 ignores extensions", version: "0", ext: map[string]string{"refstorage": "reftable"}, want: SHA1},
		{name: "v1 sha256", version: "1", ext: map[string]string{objectFormat: "sha256"}, want: SHA256},
		{name: "v1 sha1 explicit", version: "1", ext: map[string]string{objectFormat: "SHA1"}, want: SHA1},
		{
			name:    "v1 known extensions",
			version: "1",
			ext:     map[string]string{"worktreeconfig": "true", "partialclone": "origin"},
			want:    SHA1,
		},
		{
			name:    "reftable refused",
			version: "1",
			ext:     map[string]string{"refstorage": "reftable"},
			wantErr: `"refstorage"`,
		},
		{name: "unknown object format", version: "1", ext: map[string]string{objectFormat: "md5"}, wantErr: "md5"},
		{name: "version 2", version: "2", wantErr: "version"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := checkFormat(tt.version, tt.ext)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}

				return
			}
			if err != nil || got.Name != tt.want.Name {
				t.Errorf("format = %s, %v; want %s", got.Name, err, tt.want.Name)
			}
		})
	}
}
