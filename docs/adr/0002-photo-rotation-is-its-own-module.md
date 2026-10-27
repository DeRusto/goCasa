---
status: accepted
---

# The photo rotation is its own module

The screensaver's photo rotation gets a module of its own, which partly supersedes ADR 0001. In October 2026 triage, Photo pairs (#96) and captions (#94) made each rotation step one or two photos, each with a caption. With the rotation spread across immich.go, fallback.go and device.go, each of those features would be split over all three files. Their main promises ("every photo exactly once per round", "only portraits pair") could then be tested only through HTTP with a fake Immich server.

- **The interface:**
  - `screensaverStep(now, household, portrait) → (step, left)`, where a step is one or two photos, each with its address and caption. The shared deck serves every Wall display, so the screen says whether it is portrait: a portrait screen shows a Photo pair one photo after the other.
  - `upcoming(n)` for the prefetcher.
  - The screensaver page, `/screensaver/photo` and the prefetcher call only these.
- **What it hides:**
  - the no-repeat shuffle and its state
  - Immich or the Fallback folder
  - preferring cached photos
  - photos gone since the shuffle
  - pairing, and the portrait test
  - building captions
- **Its seam:** the shuffle and pairing are a pure function over a list of candidate photos and a random source. That's a seam inside the module, so tests can check the promises directly. It is not a Go interface, because there's only one implementation.
- **Where it lives:** one file in `internal/app`, not a package. It reads the same database and Household settings as the rest of `App`.

## ADR 0001 still holds for the rest

- Night mode stays the two small functions it is.
- The screensaver handlers stay in device.go.
- Immich sync, including #91's filters, stays in immich.go.
- A "Screensaver module" over all of them still fails the deletion test.

## Considered options

- **Grow `currentPhoto` in place**, as ADR 0001 had it. This was rejected because pairing changes its input, which needs dimensions and source and not just "is the photo cached", and changes its output to one or two photos with captions. That would ripple through all three files anyway.
- **A separate Go package behind an interface.** This was rejected: there's only one implementation, so it would add a seam with nothing varying across it.

## Consequences

The screensaver page's own script is the same kind of problem, because #93, #92, #95, #96 and #94 each extend it. It gets one `show(step)` function from #93 on. It has no tests, so the gain there is keeping the code in one place, not testability.
