package client

import (
	"reflect"
	"testing"

	"go.mau.fi/whatsmeow/types"
)

func TestResolveMentions(t *testing.T) {
	lid := types.JID{User: "112233445566", Server: types.HiddenUserServer}
	lookup := func(d string) (types.JID, bool) {
		if d == "491701234567" || d == "112233445566" {
			return lid, true
		}
		return types.JID{}, false
	}
	cases := []struct {
		name, in, want string
		lookup         mentionLookup
		jids           []string
	}{
		{"none", "hello there", "hello there", nil, nil},
		{"email untouched", "mail me at a@491701234567", "mail me at a@491701234567", nil, nil},
		{"short number untouched", "room @12", "room @12", nil, nil},
		{"phone in 1:1", "hi @491701234567!", "hi @491701234567!", nil, []string{"491701234567@s.whatsapp.net"}},
		{"start of text", "@491701234567 look", "@491701234567 look", nil, []string{"491701234567@s.whatsapp.net"}},
		{"group rewrites phone to lid", "thanks @491701234567, and @491709999999", "thanks @112233445566, and @491709999999", lookup,
			[]string{"112233445566@lid", "491709999999@s.whatsapp.net"}},
		{"dedupe phone and lid", "@491701234567 @112233445566", "@112233445566 @112233445566", lookup, []string{"112233445566@lid"}},
		{"adjacent", "(@491701234567)", "(@112233445566)", lookup, []string{"112233445566@lid"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, jids := resolveMentions(tc.in, tc.lookup)
			if got != tc.want {
				t.Fatalf("text = %q, want %q", got, tc.want)
			}
			if !reflect.DeepEqual(jids, tc.jids) {
				t.Fatalf("jids = %v, want %v", jids, tc.jids)
			}
		})
	}
}
