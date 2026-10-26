---
status: partly superseded by ADR-0002 (the photo rotation)
---

# The Screensaver is not a deepening candidate

The Screensaver isn't a deepening candidate: `currentPhoto` is already its deep interface, and Night mode is two small functions with no I/O that are tested across DST. An architecture review (October 2026) proposed a "Screensaver module" over device.go, immich.go and fallback.go. We dropped it because the deletion test fails: the module would only wrap what's already deep, and wouldn't concentrate any complexity.

- **Photo choice:** `currentPhoto(now, every) → (photo, left)` hides the no-repeat shuffle, preferring cached photos, photos gone since the shuffle, and the choice between Immich and the Fallback folder. `immich_test.go` and `fallback_test.go` cover it through HTTP.
- **Night mode:** `nightModeAt` and `nightChange` total about 25 lines. The wall_display tests cover the DST gap and fold.
- **The rest:** the Today header shared with `getToday` is one struct literal. The idle fallback in `home.templ` covers only an unknown value; the real default is in migration 0018.

## Consequences

The only friction left is locality: `photoDeck` lives in immich.go though the Fallback folder uses it too, and `prefetchPhotos` reads the deck's `order` and `pos` directly. Fix that in passing (move the deck next to `screensaverPhotos` and give it an `upcoming(n)` method) when the photo code is next touched. It doesn't need an issue of its own.
