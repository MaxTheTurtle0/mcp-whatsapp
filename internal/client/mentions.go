package client

import (
	"context"
	"regexp"

	"go.mau.fi/whatsmeow/types"
)

// mentionToken matches "@<digits>" that is not glued to a preceding word
// character, e.g. "hi @491701234567," but not "mail@491701234567".
var mentionToken = regexp.MustCompile(`(^|[^\p{L}\p{N}_@])@(\d{5,20})\b`)

// mentionLookup answers which JID a digit string refers to in a chat. It is a
// seam for tests; production uses groupMentionLookup.
type mentionLookup func(digits string) (types.JID, bool)

// resolveMentions turns every "@<digits>" in text into a real WhatsApp
// mention. digits may be a phone number or a LID user part (agents copy
// either from sender fields). In a group the participant list decides the
// JID, so the tag uses the address form the group runs on (phone or LID);
// the digits in the text are rewritten to match, because WhatsApp clients
// render a mention only when the text carries "@<user of the mentioned JID>".
// Outside groups, or for digits that match no participant, the digits are
// taken as a phone number. Returns the possibly rewritten text and the
// mentioned JIDs (deduplicated, in order of first appearance).
func resolveMentions(text string, lookup mentionLookup) (string, []string) {
	if !mentionToken.MatchString(text) {
		return text, nil
	}
	var mentioned []string
	seen := map[string]bool{}
	out := mentionToken.ReplaceAllStringFunc(text, func(m string) string {
		sub := mentionToken.FindStringSubmatch(m)
		prefix, digits := sub[1], sub[2]
		jid := types.JID{User: digits, Server: types.DefaultUserServer}
		if lookup != nil {
			if j, ok := lookup(digits); ok {
				jid = j
			}
		}
		if s := jid.String(); !seen[s] {
			seen[s] = true
			mentioned = append(mentioned, s)
		}
		return prefix + "@" + jid.User
	})
	return out, mentioned
}

// mentionLookupFor builds the lookup for chat. For groups it fetches the
// participant list once; failures degrade to phone-number mentions.
func (c *Client) mentionLookupFor(ctx context.Context, chat types.JID, text string) mentionLookup {
	if chat.Server != types.GroupServer || !mentionToken.MatchString(text) {
		return nil
	}
	info, err := c.wa.GetGroupInfo(ctx, chat)
	if err != nil {
		c.log.Warnf("Mentions: group info for %s unavailable, tagging by phone number: %v", c.redactor.JID(chat.String()), err)
		return nil
	}
	byDigits := map[string]types.JID{}
	for _, p := range info.Participants {
		primary := p.JID
		if primary.IsEmpty() {
			continue
		}
		primary = primary.ToNonAD()
		for _, alias := range []types.JID{p.JID, p.PhoneNumber, p.LID} {
			if !alias.IsEmpty() {
				byDigits[alias.User] = primary
			}
		}
	}
	lidGroup := info.AddressingMode == types.AddressingModeLID
	return func(digits string) (types.JID, bool) {
		if j, ok := byDigits[digits]; ok {
			return j, true
		}
		if lidGroup && c.wa.Store != nil && c.wa.Store.LIDs != nil {
			pn := types.JID{User: digits, Server: types.DefaultUserServer}
			if lid, err := c.wa.Store.LIDs.GetLIDForPN(ctx, pn); err == nil && !lid.IsEmpty() {
				return lid, true
			}
		}
		return types.JID{}, false
	}
}

// applyMentions resolves mentions in text for chat; see resolveMentions.
func (c *Client) applyMentions(ctx context.Context, chat types.JID, text string) (string, []string) {
	return resolveMentions(text, c.mentionLookupFor(ctx, chat, text))
}
