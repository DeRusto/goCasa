# Immich API for a proxied screensaver

Research for issue #5. Question: which Immich endpoints list assets by album and by person, which return image bytes, what API-key permissions are the least we need, how pagination works, and what metadata we can store in `photos_cache` (spec §6, §10).

**Version:** everything here was checked against Immich **v3.3.3** (released 2026-10-14), the latest release when this was written. Sources are pinned to that tag:

- OpenAPI spec: https://github.com/immich-app/immich/blob/v3.3.3/open-api/immich-openapi-specs.json
- Permission enum: https://github.com/immich-app/immich/blob/v3.3.3/server/src/enum.ts
- Auth / API-key check: https://github.com/immich-app/immich/blob/v3.3.3/server/src/services/auth.service.ts, https://github.com/immich-app/immich/blob/v3.3.3/server/src/utils/access.ts
- Search: https://github.com/immich-app/immich/blob/v3.3.3/server/src/services/search.service.ts, https://github.com/immich-app/immich/blob/v3.3.3/server/src/utils/database.ts
- Media: https://github.com/immich-app/immich/blob/v3.3.3/server/src/services/asset-media.service.ts, https://github.com/immich-app/immich/blob/v3.3.3/server/src/controllers/asset-media.controller.ts

## Short answer

- **List assets:** `POST /api/search/metadata` with `filter.albumIds` and/or `filter.personIds`. Its cursor-paged response gives each asset's ID, timestamps, dimensions and a thumbhash, plus EXIF and people when you ask for them. `GET /albums/{id}` no longer returns assets in v3.
- **Image bytes:** `GET /api/assets/{id}/thumbnail?size=preview` (or `thumbnail` / `fullsize`). Never use `size=original`.
- **Least-privilege API key:** `asset.read` + `asset.view`. Add `album.read` and `person.read` only if the settings UI should list albums or people by name. Do not grant `asset.download` unless we serve originals.
- **Auth header:** `x-api-key: <key>`. The server also accepts `?apiKey=`, but keep the key in the header so it never ends up in logs.

## Authentication and permissions

- Security schemes in the spec: `api_key` (header `x-api-key`), `bearer`, and the `immich_access_token` cookie. `auth.service.ts` also reads the key from the `apiKey` query parameter (`ImmichQuery.ApiKey` in `enum.ts`).
- Each endpoint declares one required permission (`x-immich-permission` in the spec, `@Authenticated({ permission })` in the controllers). With an API key, the guard calls `isGranted({ requested: [permission], current: apiKey.permissions })` and returns `403 Missing required permission: <perm>` if the permission is missing (`auth.service.ts` around line 240). A key that holds `all` passes every check (`access.ts`, `isGranted`).
- A key is created with `POST /api/api-keys` `{ name, permissions: Permission[] }` (`ApiKeyCreateDto`, which needs at least one permission). The Immich web UI lets you pick the permissions when you create a key.
- **The permission only opens the endpoint. Which rows you see is decided separately.** After the permission check, the server checks access as the key's owning user: their own assets, assets in albums shared with them, and partner assets (`access.ts`, the `AssetRead` and `AssetView` cases). So the key sees whatever its user can see. Albums and people we want to show must be owned by, or shared with, that user.

| Endpoint | Method | Permission | Use |
|---|---|---|---|
| `/search/metadata` | POST | `asset.read` | list assets by album/person, paged |
| `/search/random` | POST | `asset.read` | random sample with the same filters, no paging |
| `/assets/{id}` | GET | `asset.read` | metadata for one asset |
| `/assets/{id}/thumbnail` | GET | `asset.view` | thumbnail / preview / fullsize bytes |
| `/assets/{id}/original` | GET | `asset.download` | original file bytes (not needed) |
| `/albums` | GET | `album.read` | list albums (settings picker) |
| `/albums/{id}` | GET | `album.read` | album info, including `assetCount` and `updatedAt` |
| `/people` | GET | `person.read` | list people (settings picker), paged by `page`/`size` (max 1000) |
| `/server/version` | GET | none | health/version probe |

Note: the album-access check that search runs when you filter by `albumIds` (`requireAccess(... Permission.AlbumRead)`) is the user-level access check, not the key's permission check. So a key with only `asset.read` can still search an album it knows the ID of.

## Listing assets by album and/or person

`POST /api/search/metadata`, body `MetadataSearchDto`. In **v3.2.0** a new request shape was added (`filter`, `cursor`, `orderBy`), and the old flat fields (`albumIds`, `personIds`, `page`, `takenAfter`, …) were marked deprecated. `isNewShapeRequest()` sends the request to the new code path when you use the new shape.

```json
{
  "filter": {
    "or": [
      { "albumIds": { "any": ["<album-uuid>"] } },
      { "personIds": { "any": ["<person-uuid>"] } }
    ],
    "type": { "eq": "IMAGE" },
    "visibility": { "eq": "timeline" },
    "trashedAt": { "eq": null }
  },
  "orderBy": { "field": "fileCreatedAt", "direction": "desc" },
  "size": 250,
  "withExif": true,
  "withPeople": true
}
```

- `IdsFilter` supports `any` / `all` / `none`. `or` takes branches of `SearchFilterBranch`. Use it to get "album OR person". Put two filters at the top level instead and you get AND.
- `orderBy.field` is one of `fileCreatedAt` (the default), `localDateTime`, `fileSizeInBytes`, `rating`. Results always tie-break on `asset.id`, so the order is deterministic.
- `POST /api/search/random` takes the same `filter`, `size` (max 1000), `withExif` and `withPeople`. It returns `AssetResponseDto[]` in `ORDER BY random()` with no cursor. This could replace keeping a full list if we only want a shuffled sample on each refresh.

### Pagination

- `size`: default 250, max 1000.
- The response is `{ assets: { items, count, total, nextCursor, facets }, albums: {...} }`. To get the next page, send the same body with `cursor: nextCursor`. `nextCursor` is `null` on the last page.
- The cursor is just an **encoded offset** (`encodeSearchCursor(offset + size)`, and the query uses `LIMIT size+1 OFFSET n`). If assets are added or removed while you page through, items can be skipped or repeated. That is fine for a screensaver refresh.
- **Gotcha:** `total` and `count` both equal the number of items **in this page** (`mapResponse` sets both to `assets.length`). Neither is the total number of matches. To get a true count, use `GET /albums/{id}` `assetCount` or `POST /search/statistics`.

### Gotcha: the new filter shape has no default exclusions

The old path removed trashed assets by default (`withDeleted` false, so `deletedAt is null`) and removed locked assets. The v3 `searchAssetBuilder` only removes **locked assets that belong to other users**. It does **not** remove trashed assets, and it does not remove `hidden` (live-photo video halves) or `archive` assets unless you ask (`database.ts`, `searchAssetBuilder` / `branchPredicates`). Always send `trashedAt: { eq: null }` and a `visibility` filter (`eq: timeline`, or `in: [timeline, archive]` if archived photos should appear). Also send `type: { eq: IMAGE }` to skip videos. This comes from reading the source, so check it against a live server once.

## Image bytes

`GET /api/assets/{id}/thumbnail?size=<AssetMediaSize>`. Permission is `asset.view`. The response is `application/octet-stream`, the real image `Content-Type` comes from the file extension, and caching is `CacheControl.PrivateWithCache`.

- `size` is one of `thumbnail` (the default, small grid tile), `preview` (large, made for the viewer), `fullsize`, or `original`. Their pixel sizes and formats are set in Immich's admin Image settings.
- `size=original` on this endpoint is deprecated. The controller redirects it to `/assets/{id}/original`.
- **Redirect gotcha:** for `size=fullsize`, if the original is a web-supported image, Immich responds with a **redirect to `/original`**, and that endpoint needs `asset.download`. If no fullsize file exists yet, it redirects to `size=preview`. The endpoint description says so directly ("Viewing the fullsize thumbnail might redirect to downloadAsset, which requires a different permission"). **Use `size=preview` for the screensaver.** It is the right resolution for a wall display, never redirects to `/original`, and needs only `asset.view`. If the proxy does follow redirects, it must send the `x-api-key` header again.
- `edited=true` returns the edited version if one exists.
- `GET /api/assets/{id}/original` (permission `asset.download`) streams the original or edited file. We don't need it.

## Metadata available for `photos_cache`

From `AssetResponseDto`, which comes back in search results:

| Field | Notes |
|---|---|
| `id` | asset UUID, the proxy key |
| `type` | `IMAGE` / `VIDEO` / `AUDIO` / `OTHER` |
| `fileCreatedAt` | UTC capture time. Use this for "taken at". |
| `localDateTime` | capture time in the photographer's local time, with no timezone (for display) |
| `createdAt` / `updatedAt` | upload time / last DB update (use for change detection) |
| `width`, `height` | dimensions (for orientation and layout) |
| `thumbhash` | base64 placeholder. Immich also uses it as the cache-buster `c` query parameter on thumbnails, so it changes when the thumbnail is regenerated. |
| `originalMimeType`, `originalFileName`, `isFavorite`, `visibility`, `isTrashed`, `ownerId` | |
| `exifInfo` (needs `withExif: true`) | `dateTimeOriginal`, `timeZone`, `city`/`state`/`country`, `latitude`/`longitude`, `description`, `rating`, `orientation`, `exifImageWidth`/`Height`, camera fields |
| `people` (needs `withPeople: true`) | `PersonResponseDto[]`: `id`, `name`, `isHidden`, `birthDate`, … |

## Other notes

- The album and person filters only see what the key's user can access. A family dashboard should get a key from the account that owns, or is shared, the screensaver albums.
- `/timeline/buckets` and `/timeline/bucket` also filter by `albumId` and `personId`, but the spec marks them **`Internal`**, so they are not a stable API. Avoid them.
- Many v2 fields are marked `Deprecated` in v3 (for example the flat search fields and `nextPage`). Code against the v3.2+ shape. If GoCasa has to support Immich older than v3.2, it would need the legacy flat body (`albumIds`, `personIds`, `page`, `nextPage`), which v3.3.3 still accepts.
