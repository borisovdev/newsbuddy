package feed

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// ItemKey returns the dedup key and whether the item is usable at all.
// rawPubDate is the ORIGINAL date string from the feed (gofeed Item.Published),
// not a reformatted parse — reformatting would change the hash across
// parser versions.
func ItemKey(guid, resolvedLink, title, rawPubDate string) (key string, ok bool) {
	if trimmedGUID := strings.TrimSpace(guid); trimmedGUID != "" {
		return trimmedGUID, true
	}

	if resolvedLink != "" {
		return resolvedLink, true
	}

	trimmedTitle := strings.TrimSpace(title)
	if trimmedTitle != "" {
		sum := sha256.Sum256([]byte(trimmedTitle + "|" + rawPubDate))
		return hex.EncodeToString(sum[:]), true
	}

	return "", false
}
