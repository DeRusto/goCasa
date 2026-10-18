# Mealie shopping-list API: pull, check-off, add, round-trip fidelity

Answers [issue #6](https://github.com/DeRusto/goCasa/issues/6). Spec refs: `skylite-go-spec.md` §6 (`shopping_items.extra_json`) and §10 (Mealie mirror).

**Version:** everything below was read from Mealie source at tag **v3.29.0** (released 2026-10-17, commit `89a1420`). Short links use `SRC = https://github.com/mealie-recipes/mealie/blob/v3.29.0/`. The interactive OpenAPI docs of any instance are at `/docs` and the raw spec at `/openapi.json`. Check those against the target instance's version, because the shopping-list schema has changed between majors (households were added in v2).

## TL;DR

- **Auth:** a long-lived API token sent as `Authorization: Bearer <token>`. Tokens are created per user (`POST /api/users/api-tokens`, or in the UI under Profile > API Tokens) and only see that user's **household**.
- **Pull:** `GET /api/households/shopping/lists` returns summaries without items. `GET /api/households/shopping/lists/{id}` returns the list with every item embedded (`listItems`). Alternatively `GET /api/households/shopping/items?queryFilter=shoppingListId="<id>"&perPage=-1`.
- **Check-off / edit:** `PUT /api/households/shopping/items/{id}` (or bulk `PUT /api/households/shopping/items` with an array). This is a **full replace**, not a patch: every field you leave out is reset to its default (see Pitfalls).
- **Add:** `POST /api/households/shopping/items` (or `/create-bulk`). Mealie may **merge** the new item into an existing unchecked one instead of creating it. Read the `createdItems` / `updatedItems` arrays in the response.
- **Change feed:** none. There is no SSE, websocket, `since` cursor or ETag. You must **poll**. Optional push hint: an Apprise notifier with a `json://` / `jsons://` URL fires on `shopping_list_updated` with the list id and changed item ids.
- **Round-trip rule:** GET the item, change only `checked` (or whatever), and PUT the **whole item back**, including `recipeReferences` (with their `id`s), `extras`, `foodId`, `unitId`, `labelId`, `note`, `quantity`, `position`, `display`.

## Endpoints

All routes sit under the `/api` prefix ([SRC mealie/routes/__init__.py#L20](https://github.com/mealie-recipes/mealie/blob/v3.29.0/mealie/routes/__init__.py#L20)). Routes are defined in [SRC mealie/routes/households/controller_shopping_lists.py](https://github.com/mealie-recipes/mealie/blob/v3.29.0/mealie/routes/households/controller_shopping_lists.py).

### Lists (`/api/households/shopping/lists`, L156)

| Method | Path | Body / response | Notes |
|---|---|---|---|
| GET | `` | `PaginationQuery` -> `ShoppingListPagination` of `ShoppingListSummary` | No items. L176 |
| POST | `` | `ShoppingListCreate` -> `ShoppingListOut` | L186 |
| GET | `/{id}` | -> `ShoppingListOut` (with `listItems`, `recipeReferences`, `labelSettings`) | **The main pull call.** L200 |
| PUT | `/{id}` | `ShoppingListUpdate` -> `ShoppingListOut` | Rename and list extras. L204 |
| DELETE | `/{id}` | -> `ShoppingListOut` | L217 |
| PUT | `/{id}/label-settings` | `[ShoppingListMultiPurposeLabelUpdate]` | Label section ordering. L234 |
| POST | `/{id}/recipe` | `[{recipeId, recipeIncrementQuantity, recipeIngredients?}]` | Add a recipe's ingredients. L256 |
| POST | `/{id}/recipe/{recipeId}/delete` | `{recipeDecrementQuantity}` | L274 |

### Items (`/api/households/shopping/items`, L38)

| Method | Path | Body -> response | Notes |
|---|---|---|---|
| GET | `` | `PaginationQuery` -> `ShoppingListItemPagination` | Filter with `queryFilter`. L115 |
| GET | `/{id}` | -> `ShoppingListItemOut` | L131 |
| POST | `` | `ShoppingListItemCreate` -> `ShoppingListItemsCollectionOut` | Wraps create-bulk. L127 |
| POST | `/create-bulk` | `[ShoppingListItemCreate]` -> `ShoppingListItemsCollectionOut` | L121 |
| PUT | `/{id}` | `ShoppingListItemUpdate` -> `ShoppingListItemsCollectionOut` | Wraps the bulk update. L141 |
| PUT | `` | `[ShoppingListItemUpdateBulk]` (each has `id`) -> `ShoppingListItemsCollectionOut` | L135 |
| DELETE | `/{id}` | -> `SuccessResponse` | L151 |
| DELETE | `?ids=..&ids=..` | -> `SuccessResponse` | L145 |

Writes return `ShoppingListItemsCollectionOut = {createdItems, updatedItems, deletedItems}` ([schema L149-154](https://github.com/mealie-recipes/mealie/blob/v3.29.0/mealie/schema/household/group_shopping_list.py#L149-L154)), **not** the single item. The client has to read all three arrays.

Pagination parameters (`page`, `perPage` default 50 with `-1` for all, `orderBy`, `queryFilter`) come from [SRC mealie/schema/response/pagination.py](https://github.com/mealie-recipes/mealie/blob/v3.29.0/mealie/schema/response/pagination.py).

## Auth

- FastAPI `OAuth2PasswordBearer(tokenUrl="/api/auth/token")`. A bearer value is accepted either as a session JWT or as a long-lived API token, validated by `validate_long_live_token` ([SRC mealie/core/dependencies/dependencies.py#L23, L102-L112, L159](https://github.com/mealie-recipes/mealie/blob/v3.29.0/mealie/core/dependencies/dependencies.py#L102)).
- Tokens are created with `POST /api/users/api-tokens` and revoked with `DELETE /api/users/api-tokens/{id}` ([SRC mealie/routes/users/api_tokens.py#L21, L49](https://github.com/mealie-recipes/mealie/blob/v3.29.0/mealie/routes/users/api_tokens.py#L21)). The user can also make one in the UI. GoCasa should store the token AES-GCM encrypted. Don't store a password.
- Scope: repositories filter by the token user's group and household, so a token only sees that household's lists. One integration means one household.

## Item schema (wire format, camelCase)

All Mealie models use `alias_generator=camelize, populate_by_name=True` ([SRC mealie/schema/_mealie/mealie_model.py#L53](https://github.com/mealie-recipes/mealie/blob/v3.29.0/mealie/schema/_mealie/mealie_model.py#L53)). Mealie accepts camelCase and snake_case and returns camelCase.

From [SRC mealie/schema/household/group_shopping_list.py](https://github.com/mealie-recipes/mealie/blob/v3.29.0/mealie/schema/household/group_shopping_list.py) (L58-147) and `RecipeIngredientBase` ([SRC mealie/schema/recipe/recipe_ingredient.py#L312-L330](https://github.com/mealie-recipes/mealie/blob/v3.29.0/mealie/schema/recipe/recipe_ingredient.py#L312-L330)):

| Field | Type | Default when omitted on write | Notes |
|---|---|---|---|
| `id` | UUID | generated (create); required in bulk PUT | Create accepts a client-supplied id. |
| `shoppingListId` | UUID | **required** | Also on PUT. |
| `checked` | bool | `false` | |
| `position` | int | `0` | |
| `quantity` | float | `1` | `< 0` on PUT **deletes** the item. |
| `note` | string | `""` | The free-text name when there is no food. |
| `display` | string | auto-computed | Recomputed from qty/unit/food/note when empty. |
| `foodId` / `food` | UUID / object | `null` | Writes use `foodId`; `food` is an expanded read-only object. |
| `unitId` / `unit` | UUID / object | `null` | Same. |
| `labelId` / `label` | UUID / object | `null` | On read, `label` is filled in from the food's label when it is missing (L120-127). |
| `referencedRecipe` | object | `null` | Inherited from the ingredient base. Rarely set. |
| `extras` | `{string: string}` | `{}` | Arbitrary key/value. Stored as rows with a **String** value column ([api_extras.py](https://github.com/mealie-recipes/mealie/blob/v3.29.0/mealie/db/models/recipe/api_extras.py)), so treat values as strings. |
| `recipeReferences` | list | `[]` | Each `{id, shoppingListItemId, recipeId, recipeQuantity, recipeScale, recipeNote}`. On PUT you may send with or without `id`. |
| `groupId`, `householdId`, `createdAt`, `updatedAt` | read-only | n/a | `updatedAt` can drive change detection when polling. |

List-level: `ShoppingListOut` has `id, name, extras, groupId, userId, householdId, listItems[], recipeReferences[], labelSettings[]` (L180-283).

## Partial-update pitfalls (the round-trip risk)

1. **PUT is full replacement with defaults.** The controller casts the body into `ShoppingListItemUpdateBulk`. The repository does `model_dump()` with no `exclude_unset` ([SRC mealie/repos/repository_generic.py#L230-L250](https://github.com/mealie-recipes/mealie/blob/v3.29.0/mealie/repos/repository_generic.py#L230-L250)). The ORM `update()` then re-runs `__init__` with every field ([SRC mealie/db/models/_model_base.py#L41-L48](https://github.com/mealie-recipes/mealie/blob/v3.29.0/mealie/db/models/_model_base.py#L41-L48)). So `PUT {"shoppingListId": X, "checked": true}` would:
   - reset `quantity` to 1, `note` to "", `position` to 0
   - null out `foodId`, `unitId`, `labelId`
   - **wipe `extras`** (`{}` is converted to an empty extras list by `@api_extras`)
   - **wipe `recipeReferences`** (`_model_base.update` explicitly sets empty lists)
   - recompute `display`

   Losing recipe references also cascades: `remove_unused_recipe_references` then drops the list-level recipe reference ([service L132-150](https://github.com/mealie-recipes/mealie/blob/v3.29.0/mealie/services/household_services/shopping_lists.py#L132-L150)).
2. **Fields that must survive a round trip.** Send these back exactly as read: `shoppingListId, checked, position, quantity, note, display, foodId, unitId, labelId, extras, recipeReferences` (keep each ref's `id`), plus `id` in the bulk form. The expanded `food`, `unit` and `label` objects can be echoed back harmlessly; the PUT schema accepts them (inherited from `RecipeIngredientBase`). For GoCasa, the simplest correct approach is to store **the entire `ShoppingListItemOut` JSON** in `extra_json`, overlay only GoCasa-owned fields (`checked`, `note`, `quantity`, `position`) and PUT the result. Don't try to pick out "extra" keys.
3. **Merging on add, and on un-check.** `bulk_create_items` and `bulk_update_items` merge an item into an existing **unchecked** item on the same list when `foodId` matches (or both lack a food and their `note`s are equal), and the units are equal or convertible ([`can_merge` L47-73](https://github.com/mealie-recipes/mealie/blob/v3.29.0/mealie/services/household_services/shopping_lists.py#L47-L73), [`merge_items` L75-130](https://github.com/mealie-recipes/mealie/blob/v3.29.0/mealie/services/household_services/shopping_lists.py#L75-L130)). Consequences:
   - A POST can return the new data in `updatedItems` under a **different id**, with an empty `createdItems`. Quantities are summed, notes are joined with ` | `, and extras are merged.
   - Un-checking an item (PUT `checked:false`) can merge it into a sibling. Its id then appears in `deletedItems`. The local mirror must drop or remap that id.
   - Checked items never merge.
4. **Negative quantity deletes** on PUT, and is silently dropped on POST (service L287, L368).

## Change feed vs polling

- No pull-side change feed: there are no `since`/`updatedAfter` endpoints, no ETag/304, and no websocket or SSE routes for shopping lists.
- Polling: `GET /lists/{id}` per mirrored list, or one items query across lists. A cheaper delta poll is possible with `queryFilter=updatedAt > "<iso>"`, but **deletes are invisible** to a delta poll. A periodic full fetch is needed to detect removals, so plan on a full list GET each interval and diff by id.
- Push hint (optional): Mealie publishes `shopping_list_created/updated/deleted` events with `{shoppingListId, shoppingListItemIds, operation}` ([controller L41-L100](https://github.com/mealie-recipes/mealie/blob/v3.29.0/mealie/routes/households/controller_shopping_lists.py#L41-L100), [event_types.py](https://github.com/mealie-recipes/mealie/blob/v3.29.0/mealie/services/event_bus_service/event_types.py)). These only reach **Apprise notifiers** (household Settings > Notifiers, with per-event toggles). With a `json://`/`jsons://`/`form://` URL the event data is added as `:event_type`, `:document_data` etc. payload keys ([event_bus_listeners.py L73-L132](https://github.com/mealie-recipes/mealie/blob/v3.29.0/mealie/services/event_bus_service/event_bus_listeners.py#L73-L132)). Mealie's "webhooks" feature is **only** for scheduled meal-plan posts (L135-168), not list changes. A notifier could trigger an immediate GoCasa re-pull, but it needs a manual setup step in Mealie and a reachable GoCasa endpoint. Treat it as an optimization; polling stays the source of truth.

## Not verified

- Behaviour was read from source, not tested against a running v3.29.0 instance. Spec §10 already requires a live round-trip test.
- Older Mealie (v1.x, pre-households) used `/api/groups/shopping/...` paths. This doc doesn't cover them.
