# messwa

messwa links to WhatsApp as a companion device (via [whatsmeow](https://github.com/tulir/whatsmeow)), merges the mess buy/sell groups you pick into one deduplicated list of listings, and serves that list plus limited DM info to an agent over MCP.

The WhatsApp access limits live in the binary. Ingest drops every chat outside the allowed set before anything touches disk, and WhatsApp MCP tools read only that stored data. Optional portal tools separately read your own registrations and current QR, using the mess API key saved on your messwa account.

By default the `run` service and MCP tools are read-only: no messages, read receipts or presence are sent. Your phone keeps showing messages as unread and notifications don't change. Two things can send, and both are off until you ask for them: the explicitly armed `deliver-qr` command, and sell tasks, which a user starts over MCP after enabling sell tasks on their account page (see [Sell tasks](#sell-tasks)). After a sale, both react ✅ to their own offer posts. `mark-sold` sends only that ✅, after recording a sale you finished yourself.

## What gets stored

| Source | Stored |
| --- | --- |
| The configured mess groups | Posts that look like a listing (a sell/buy phrase or a meal name), reactions, edits, deletes. Pruned after 14 days |
| Ended listings, kept for good | One row each in `listing_facts`: seller's number and name, mess, meal, date, diet, price, when it was posted and sold. No message text |
| DMs with saved contacts | Name and last-message times only |
| DMs with unsaved numbers | Name, times, and message text (deleted after `dms.retention`, default 72h) |
| Sell tasks' buyers | What the bot read from each message (an offer of ₹15, a question, a picture, "not buying"), the bot's own replies, and payment steps. Never the buyer's words, saved contact or not |
| Every other group, status, channel, notes-to-self | Nothing |

If you save a number later, its stored DM text is deleted on its next message, and the MCP refuses to return it immediately.

## How listings work

**Dedupe.** Posts from one person for the same meal serving are one listing, however they're worded and across any number of groups. People post tomorrow's breakfast the evening before and again at 3am, so the lookback is 24 hours. Posts naming different messes, or veg and non-veg, stay separate. A sold or withdrawn listing never absorbs a new post, so a repost after a buyer backs out shows as available.

**Meal.** If the post names a meal ("lunch", "bf", "nashta"...), that meal's next serving wins. Otherwise it's the meal currently being served or the next one: a post at 10:00 is for lunch, one at 22:00 is for tomorrow's breakfast. `meal_source` says which rule applied.

**Status.** Status is computed when the agent asks, so un-reacting un-sells a listing.

- `sold` means the seller reacted to any copy with ✅, ✔, ☑ or 👍, or edited the post to say "sold", or replied "sold" to it. Ticks from anyone else don't count, but `other_ticks` reports them. Admins never tick other people's posts in these groups, so their ticks are ignored unless you set `admins_count = true`.
- `closed` means the seller reacted with ❌ to withdraw it.
- `expired` means the meal window has closed.
- `deleted` means every copy was deleted (shown only with `status: "all"`).
- `available` covers everything else.

Mess (Kadamba, Yuktahar, Palash, Bakul) and diet (veg, nonveg, egg) are tagged when the text mentions them, with typo tolerance. Palash and Yuktahar serve only veg, so their listings are always veg. Prices ("@30", "30/-", "free"), quantities ("x2") and exchanges ("have palash want kadamba") are parsed too, and a seller replying to their own post with a new price updates the listing.

## Accounts

Everyone signs in to the dashboard with their own messwa account. There are two roles:

| Feature | Admin | Member |
| --- | --- | --- |
| Shared board, market pulse and listing details | yes | yes |
| Own WhatsApp link, QR, status and unlink | yes | yes |
| Own sales, buyers, regulars, earnings and registrations | yes | yes |
| Own DMs and saved-contact checks over MCP | yes | yes |
| Own sell tasks and operator actions, when enabled | yes | yes |
| Own deliver-qr and mark-sold, through CLI shell access | yes | yes |
| Own mess key, password, MCP tokens and selling settings | yes | yes |
| insights.html, including every seller's market record | yes | no |
| Market and calendar settings | yes | no |
| Account management | CLI shell access | CLI shell access |

Each messwa user has zero or one linked WhatsApp account. Open `/account.html`, choose Link WhatsApp, and scan the server-generated QR from WhatsApp's Linked devices screen. The page polls connection status and refreshes the QR while pairing. Unlink stops your active sell tasks and logs your device out. Other users, including admins, cannot read or act on your private data through the dashboard or MCP. Pairing codes are available through `login --user NAME --phone NUMBER`; the dashboard currently supports QR only.

The service starts each user's linked device independently. WhatsApp's reconnect handling and startup retries belong to that client; a failed connection or logout does not stop the service or another account. Users need not belong to every configured group. Group ingest checks the global allowlist separately on each client. The shared group log deduplicates by group JID, message ID and event type, and posts by group JID and message ID.

**Private storage and upgrades.** The first enabled admin at upgrade owns the old `state/session.db`, all existing private rows in `state/messwa.db`, and the old selling settings. A persistent `device_ownership` mapping pins that assignment even after role changes or deletion. Existing task ownership is updated in the same migration transaction. No device keys are copied or moved, so the old device reconnects without linking again. If there is no admin, assignment waits until one exists. Other users use `state/user-ID/session.db` and `state/user-ID/messwa.db`. Account IDs are never reused, including after deletion, so a new user cannot inherit an old user's directory. Back up the complete state directory. Deleted users' private files remain on disk for operator-managed retention, but their sessions and tokens cannot access them.

Only market tables are shared: groups, listings, posts, reactions, the group log, market prices and listing facts. DMs, delivery claims, payment IDs, offers, sell tasks, buyers, negotiations, event journals and settings live in the owner's private database. A task ID or payment ID can occur in two users' databases without referring to the same sale. WhatsApp contact and device keys live in that user's session database.

`login`, `groups`, `inspect-payment`, `deliver-qr`, `mark-sold`, `offer-group` and `release-handoff` accept `--user NAME`. Except for `login`, omitting it selects the first admin for CLI compatibility. Stop the service before opening the same WhatsApp session from a CLI command. Linking and unlinking through the account page work while the service runs.

Each person saves their own mess API key on the account page (`/account.html`, the ACCOUNT button on the board). The key never leaves the server again after that. The page also changes your password and creates and revokes your MCP tokens.

**Keys at rest.** Mess API keys are encrypted with AES-256-GCM. The key for that lives in `MESSWA_SECRET_KEY` (64 hex characters, `openssl rand -hex 32`) in `.env`, not in `state/`, so a copy of the state directory alone can't decrypt them. Each encrypted key is bound to its user, so copying it to another account's row doesn't decrypt either. Without `MESSWA_SECRET_KEY` messwa runs, but can't save or read mess keys. If you lose or change it, everyone has to save their key again.

**Passwords and sessions.** Passwords are hashed with argon2id. Any non-empty password works. Signing in sets an `HttpOnly`, `SameSite=Strict` session cookie that lasts 30 days. Sessions live in the database as SHA-256 hashes. Every change the pages make sends a per-session CSRF token and must come from the dashboard's own origin. Five wrong passwords for one name from one address lock that pair out for a minute. Changing a password signs out your other devices.

**Signing up.** Anyone who can reach the dashboard can make an account from the link on the sign-in page (`/signup.html`). A new account lands on `/welcome.html` and can't open anything else until an admin approves it:

1. They save their mess API key. messwa reads today's registrations with it first, so a mistyped key is refused, and saving one needs the IIIT network.
2. They link their WhatsApp by scanning a QR code, the same way as on the account page.
3. They ask for approval.
4. An admin approves or declines them under Sign-up requests on the account page, or with `messwa user approve NAME` / `decline NAME`. The admin's list and the user's welcome page update the moment either side acts, over server-sent events. The welcome page moves them to the board once approved. Decisions made with the CLI run in another process, so the welcome page catches those within a minute.

Until then the API answers 403 and their MCP tokens don't work. `run` connects a waiting account's WhatsApp only while it pairs, and doesn't reconnect it after a restart until it's approved. Approval connects it and resumes its sell tasks. If you approve from the CLI, that happens the next time the user loads a page, or at the next restart. Declining unlinks the device. A declined account can still sign in, but only to see that it was declined; remove it with `user remove`. One address can sign up five times an hour. Accounts made with `user add` skip all this and start approved.

**Managing accounts** from the shell:

```sh
./messwa user add -admin shourya    # asks for a password
./messwa user add asha              # a member
./messwa user list                  # with each account's sign-up status
./messwa user approve asha          # or decline; for accounts made on the sign-up page
./messwa user passwd asha           # set a new password, sign asha out everywhere
./messwa user role asha admin
./messwa user disable asha          # blocks sign-in and MCP tokens; enable undoes it
./messwa user remove -yes asha      # deletes the account, its key and tokens
./messwa user mess-key asha         # save a key for someone (asks for it); -clear removes it
./messwa user token -label laptop asha
./messwa user tokens
./messwa user revoke 3
```

In Docker, prefix these with `docker compose run --rm messwa`. They can run while the service is up.

### Upgrading from `MESS_API_KEY`

Before accounts, one `MESS_API_KEY` in `.env` served everything. To move over:

1. Add `MESSWA_SECRET_KEY=$(openssl rand -hex 32)` to `.env`.
2. `docker compose up -d --build`.
3. `docker compose run --rm messwa user add -admin <you>`. This encrypts `MESS_API_KEY` onto your account.
4. Remove `MESS_API_KEY` from `.env` and `docker compose up -d` again.

Until the key has moved, the first admin's portal reads (MCP, the sales page, the portal commands) still use `MESS_API_KEY`, so nothing stops working halfway. Before step 3 the sign-in page says to create an admin. If you skip step 1, step 3 creates the account but leaves the key in the environment and says so. `run` moves the key onto the first admin by itself once `MESSWA_SECRET_KEY` is set. After that the env var is ignored, and `run` logs a warning until you remove it.

## Dashboard

`run` also serves the dashboard at `dashboard.listen`. It needs a signed-in account (see [Accounts](#accounts)), and messwa refuses to listen on anything but loopback, a private LAN range, or Tailscale's `100.64.0.0/10`. It shows every seller's number, so sign-in is a second lock, not a replacement for keeping it off the internet. It's laid out like an auction house's buy-it-now browser:

- The left column picks the mess, or all of them. Badges count what's available.
- Each slot is one listing, showing its price, "?" when none was stated, FREE, or SWAP with the mess wanted. The corners show the diet mark, the meal, and the quantity. The bar along the bottom drains as the meal's serving window runs out. Listings posted in the last 2 minutes shine, and an amber dot means someone other than the seller ticked it.
- The bottom row sorts by recency or price (click again to flip), filters by diet on Kadamba and Bakul, filters by meal, shows sold and withdrawn listings, and switches to buy requests.
- Hovering a slot shows the seller, phone, groups, times and the original text. Clicking opens every copy of the post and a button that opens a draft to the seller. Nothing is sent from messwa.
- SETTINGS in the header opens the [settings page](#settings-page). Its YOU tab picks your messaging app: WhatsApp (`wa.me`) or Beeper Desktop (`beeper://compose/whatsapp/phone/...`). The message button then opens that app. The choice is saved in the browser. Beeper links only work in Beeper Desktop, so phones and tablets always open WhatsApp.
- With Beeper picked and `dashboard.beeper_db` set, each copy of a post in the listing is also a link to that message in its group (`beeper://select-thread/whatsapp/<room>/<sortKey>`). messwa finds it in Beeper's `index.db`, opened read-only: the room by the group's JID, the message by its timestamp and the seller's ID. Beeper's room IDs and sortKeys are the same on every device, so the database of a Beeper Server on this machine works for Beeper Desktop on a laptop. These groups delete messages after 24 hours, so older copies have no link. In Docker, mount the Beeper data folder with a `compose.override.yaml`:

  ```yaml
  services:
    messwa:
      volumes:
        - /home/you/.beeper/profiles/server/server:/beeper:ro
  ```

  and set `beeper_db = "/beeper/index.db"`.
- Below the board, the market pulse has a card per mess with messages per minute, sales per minute and demand (sales per message) over the last 15 minutes to 2 hours. A message is a post selling or swapping that mess. Each copy in each group counts, replies under a listing don't. A sale is the seller's tick or "sold". The strip shows messages (faint) and sales (solid) per 5 minutes over the last 2 hours, on one scale for every mess. The bottom row has the median asking price, the median time from post to sale, and buy orders per minute. HOT and SLOW compare a mess's demand with the whole market's. Clicking a card browses that mess.

Keys: `1`-`5` messes, arrows for pages, `R` `P` `D` `M` `S` `W` for the bottom row, `/` to search. Right click on the diet or meal button, or Shift with `D` or `M`, cycles backwards.

The board shows sellers' names and phone numbers from the groups. From DMs it shows each user only their own sell tasks' buyers (see [My sale](#my-sale)). The sales page below adds the names and numbers of people you sold to, for that user only.

### My sale

While a [sell task](#sell-tasks) runs, users get a MY SALE box above the board, built to read in one look on a phone. It stays 20 minutes after the task ends, so you see how it went. Hovering anything (or opening it) says what it means.

- **Glow.** Green: a buyer's pay window is open. Red: a buyer needs you, or sent a picture instead of paying. No glow: the bot has it.
- **Clock.** Minutes until the meal ends. A ✓ or ✕ once the task is over.
- **Price bar.** Your floor on the left, the opening price on the right. The green part is every price the bot takes right now, the arrow is its lowest, and the gold tick is the target it picked from the market's median ask. The lowest slides left over the last three hours; 🔒 means it's held at the target because two buyers are active. Dots are buyers' offers, hollow when the buyer needs you.
- **Posts.** One pill per group. Gold is your post and the bar after it is the listings posted in that group since, red once more than 15 have buried it. A dashed red pill failed, with WhatsApp's error code (479 and the like).
- **Buyers.** Pixel heads in reply order, the same heads as on the sales page. The border is their state: blue haggling, gold agreed and waiting, green paying (the ring is their two minutes), red needs you (!), amber check (?, usually a screenshot instead of a WhatsApp Pay payment), grey gone. ♥ marks a saved contact. The chip under a head is their price.
- **Squares, top right.** Every task for this meal, so a restart doesn't lose the story: grey crossed cancelled, green live or sold.
- **Market.** Every open listing for the same meal and mess, cheapest first, yours in gold. Your listing also gets a gold outline on the board.

Tap a buyer for the negotiation as a tug of war: each row is one round, their offer in red and the bot's answer in green, with the bot's lowest at the bottom and the gap between them in red. Below it, the payment steps (armed, paid, read, QR, ✅), with the attempts to read an unreadable payment from your phone. CHAT opens their chat in WhatsApp or Beeper. Tap the clock or the posts for the lowest price over time, each post, and the meal's tries.

Users can use **MARK SOLD** to record a sale made outside the bot (pick a buyer or enter a phone/name, price and optional note). Confirming stops negotiation, records paid/sent timestamps with payment kind `manual`, marks posts ✅ and gives other eligible buyers the usual sold handling; the chosen buyer gets no message. **ADD BUYER** queues a phone/JID with an optional name and sends the opening quote. Buyer sheets offer **MUTE** (with confirmation) and **UNMUTE**. Muted buyers keep their records and get no task messages, including sold notices or automatic QR delivery. Unmuting resumes handling their next message. Each action appears in the task event log.

The MCP tools are `mark_sell_task_sold`, `add_sell_task_buyer`, `mute_sell_task_buyer` and `unmute_sell_task_buyer`. They require your own linked WhatsApp account and sell tasks enabled for your account. The dashboard uses the same operations with session and CSRF protection. Live updates preserve forms while you type.

### Settings page

Everyone edits their own selling settings on the OFFER, REPOST and BUYERS tabs: the sell-task switch, offer groups, prices in posts, repost rules, buyer policy, handoff, image payments, unsaved chat retention and the monthly target. These settings are stored in that user's database and apply to their next actions and supported live task settings. New users start with conservative selling defaults, including image payments off. File values remain the fallback for the migrated admin. There is no configured phone number or UPI address to migrate: sender identity comes from the user's device and payment prompts use WhatsApp Pay.

`settings.html` has YOU, OFFER, REPOST and BUYERS tabs for everyone. The MARKET and CALENDAR tabs change messwa for everyone, so only admins get them:

| Tab | Settings |
| --- | --- |
| OFFER | `[mcp] sell_tasks`, `[sell] groups` or `group_count`, `post_gap`, `show_price`, `mention_extras`, `[sales] monthly_fee`, with a preview of the group post |
| REPOST | `[sell] repost`, `repost_after`, `repost_messages`, `repost_listings`, `max_reposts` |
| BUYERS | `[sell] buyers`, `handoff`, `accept_plain_dms`, `accept_payment_images`, `[dms] retention` |
| MARKET | `[sold]` emojis, closed emojis, words and `admins_count`, `[dedupe] window` |
| CALENDAR | meal times, `[[seasons]]`, each mess's `veg_only` |

Edits wait for SAVE. messwa stores what you save in a `settings` table in the user's private `messwa.db`, on top of `config.toml`: a value equal to the file's isn't stored, so it keeps following the file. Every command reads the saved values when it starts, and `deliver-qr` flags still override them for one run.

Saves made in `run`'s own dashboard apply at once: the next sell task, `sell_task_status` and running tasks' reposts, the sold rule, dedupe, pruning, the sales page target and the insights seasons. Turning sell tasks on or off adds or removes their MCP tools for connected agents. A save made from `messwa dashboard` reaches a running `run` within 30 seconds. Meal times and `veg_only` show APPLIES ON RESTART: `run` builds its listing classifier and meal schedule once, when it starts.

Some things stay in `config.toml`: the listen addresses, `state_dir`, `timezone`, `token_env`, `beeper_db`, the top-level `groups`, and meal keywords and mess names and aliases. If a saved value stops fitting the file, for example a `group_count` above the groups it now lists, messwa logs a warning, skips that value and starts anyway.

### My sales

`/sales.html` (the MY SALES button on the board) tracks the meals you sell with `deliver-qr` or a sell task. Every user sees only their own sales and registrations. On the board's day track, a coin and the price replace the meal icon for any meal you sold or armed today.

- **Sales ledger.** Days across, meals down, for the last four weeks (arrow keys page by week). A sold slot shows the price, the mess, and the buyer's pixel head. Each buyer always gets the same head. An armed slot drains like a listing until the meal ends. ATE means you used the registration yourself. WASTED means the meal ended unsold and unused. Dashed slots are registered meals that haven't ended. Hovering shows the buyer, their number, when they paid, how fast the QR went out, and the median ask for that meal.
- **Selling from the ledger.** With sell tasks on, the meal being served and the next one are outlined in gold. Click one to pick a lowest and an opening price on a two-handle slider (the tick is the median ask) and confirm. That starts a sell task, the same as `start_sell_task`. Equal handles sell at a fixed price. A meal with a task running shows SELLING and its range. Click it for the buyers so far and three buttons: MARK SOLD (you sold it yourself; `mark_sell_task_sold`), EDIT PRICES (`set_sell_task_prices`) and STOP SELLING (`cancel_sell_task`, which deletes the offer posts).
- **Friends' meals.** SELL A FRIEND'S MEAL sells someone else's meal through your WhatsApp. Pick the meal, their mess, veg, egg or non-veg, the price range and any extras they registered, and attach their QR (PNG or JPEG, up to 5 MB). The offer reads like yours, extras included, and the buyer gets the friend's QR. Each friend's meal gets its own slot, like `dinner#f1`, so it never touches your own registration, and its sale stays out of the ledger, the regulars and every total. The list shows what became of each one; click a running one to mark it sold, edit its prices or stop it. The meal and QR are kept in the `friend_meals` table. `proxy-meal.json` still works for the same thing from the command line.
- **Saved this month.** The green bar is what your sales earned back against this month's mess bill. The striped part after it is what the month's wasted meals would have fetched at the median ask. The bill comes from the portal's `/registrations/bill`, the same read as IIITH-mess-MCP's `get_bill`: availed meals plus the projected cost of meals you're registered for, so it grows as you register. When the portal can't be read, the page uses `sales.monthly_fee` from `config.toml` instead.
- **Regulars.** Every buyer, all time, with the number of meals they bought from you. Repeat buyers climb rarity tiers: 1 COMMON, 2-3 UNCOMMON, 4-6 RARE, 7-9 EPIC, 10+ LEGENDARY.
- **Price check.** Your sales over four weeks against the market's middle half and median ask for each meal, with UNDERCUT, AT MARKET or PREMIUM from your average gap to each serving's median.
- **Earnings.** Meals sold, money earned and median price per mess, with earnings per day on one shared scale.

Eaten, wasted and registered meals and the bill come from the portal, read with the signed-in person's own mess API key, so the page needs that key saved and the IIIT network, like `my_registrations`. The page reads registrations and the bill live and keeps each person's last read in memory for a minute. Without the portal it shows sales only and says why.

The median ask is the median stated price of sell listings for that serving at that mess. With fewer than 3 asks it falls back to every mess for that serving, then to that meal over four weeks. Listings are pruned after 14 days, so `run` saves each serving's asking prices every hour in `market_prices`.

Buyer names are the WhatsApp name on the payment message, or the one stored for that DM peer. Sales from before this page existed have no payment time, offer time or mess. The ledger fills in mess and diet from the portal when it can.

### Insights

`/insights.html` is for that user only. It's the INSIGHTS button on the board, which members don't see. It charts the market from `listing_facts`. Listings, posts and reactions are pruned after 14 days, so every hour `run` writes one permanent row per listing whose meal has ended. The first run after upgrading backfills whatever the last 14 days still hold. A row keeps updating for 7 days after its meal, so a late tick or un-tick still counts, and is final after that. Rows are keyed by the listing's first post, so `rebuild` updates them instead of adding copies.

Each weekday has its own menu, so the page treats "Bakul, Tuesday lunch" as one slot across weeks. The filters along the top pick a season, 4, 8 or 12 weeks of it or all of it, a mess and a diet. `?mess=bakul&metric=ask` and the like open a view directly.

**Seasons.** During a term everyone is registered with a mess, so the groups are many times busier than in a break, when only people who registered themselves eat there. Averaging the two would wreck every weekday slot, so the page shows one season at a time: today's, unless you pick an earlier one or ALL SEASONS. Ranges stop at the season's edge, so "4 weeks" on 20 Dec covers 8 to 20 Dec of the winter break. The defaults are spring term 1 Jan to 7 May, monsoon term 1 Aug to 7 Dec, and breaks in between. Set your own with `[[seasons]]` in `config.toml` (see `config.example.toml`). Changing them re-tags old data too, since a listing's season comes from its meal date.

- **Overview.** Days collected toward four weeks, which is four servings of every slot, then listings, sell-through, median time to sale, median ask, buy requests and sellers.
- **Terms vs breaks.** Listings and buy requests a day, sell-through and average ask for each of the last six seasons, under the mess and diet filters. Per-day numbers divide by the days messwa has data for, not the season's length, and leave out today, which isn't over yet.
- **Weekly menu board.** Weekdays across, meals down, one grid per mess, colored by sold %, listings per serving, median ask, or buy requests per serving. ×N under a day is how many of that weekday the range covers. Clicking a slot shows it week by week. The easiest and hardest slots to sell are listed beside it, counting only slots with 6 or more listings.
- **Veg vs non-veg.** Sell-through by diet for each meal at messes that serve non-veg.
- **Rush hours.** Listings, sales and buy requests per hour, averaged per day, over the serving windows.
- **Seller's playbook.** Time from post to sale, and sell-through by how long before the meal ends a listing went up, how often it was posted, its ask against that serving's median, and how many groups it was in.
- **Sellers.** New and returning sellers per day (per week past 35 days), listings per seller, and the 15 busiest sellers with their sold rate, usual slot and median ask. NEVER TICKS marks sellers with 3 or more listings and no ticks. Their sales don't show, so they pull sell-through down.

Sold means what it means on the board, the seller's tick or "sold". Deleted listings are left out. `messwa dashboard` writes any missing rows when it starts, so the page works without the live service.

To preview without the live service, `messwa dashboard -at "2026-11-02 12:45"` serves the board as it stood at that time on `127.0.0.1:8768`. Sold status reflects reactions up to now, not up to that time.

## MCP tools

| Tool | Returns |
| --- | --- |
| `meal_schedule` | Now, current/next meal, today's windows |
| `list_listings` | Listings filtered by kind, status, meal, mess, hours. Default: available sells, last 24h |
| `get_listing` | One listing with every copy across groups |
| `list_recent_chats` | DM peers in the last 24h (max 72h): saved or not, new or not, last in/out. No content. Your account only |
| `read_unsaved_chat` | Message text for one unsaved number. Errors for saved contacts. Your account only |
| `my_registrations` | Live portal registrations for a date range, including cancellation and usage fields. Uses the caller's own mess API key |
| `my_qr` | Current portal QR as a PNG image. Sensitive; fetch again after token resets. Uses the caller's own mess API key |
| `start_sell_task` | Only with `[mcp] sell_tasks = true`. Sells one of your registered meals: date, meal, `min_price`, `max_price` in rupees, negotiating in between. Your account only |
| `sell_task_status` | Only with `sell_tasks` on. One task by id, or the active and last 24 hours' tasks: state, posts, buyers and where each stands in the haggle, buyers who need you, sale. Your account only |
| `sell_task_events` | Only with `sell_tasks` on. Sell task events in a time window (`since`, `until`) or for one task (`id`), to the second: copies posted or failed, buyers joining, what the bot read and said, prices, payments, QRs, the end. Also each task in the window and its current state. For reviewing a sale or a test run afterwards. Your account only |
| `cancel_sell_task` | Only with `sell_tasks` on. Stops a task and deletes its group posts, unless `keep_posts` is set. Your account only |
| `set_sell_task_prices` | Only with `sell_tasks` on. Changes an active task's `min_price` and `max_price` in place, without reposting (see [Changing prices](#changing-prices)). Your account only |

Every tool except `start_sell_task`, `cancel_sell_task` and `set_sell_task_prices` is read-only, and those three don't exist unless you turn them on.

Each MCP request acts as a messwa user. A client sends `Authorization: Bearer mwa_...`, a token made on the account page or with `messwa user token`. Tokens are stored as SHA-256 hashes, shown once, and stop working on the next request after they're revoked or their user is disabled. Two more ways in act as the owner (the first admin):

- No `Authorization` header, allowed only when `mcp.listen` is loopback and `MESSWA_MCP_TOKEN` is unset. This is how the Docker setup works today.
- The shared `MESSWA_MCP_TOKEN`, kept so existing clients don't break. Setting it turns tokenless access off. Group posts and DM text are written by strangers. The server's instructions tell the agent to treat them as untrusted, but read-only tools are what actually limits the damage. Sell tasks are off by default for that reason. When on, the rules in [Sell tasks](#sell-tasks) hold whatever the agent asks for.

### Read your portal registrations

Registration reads reuse the API contract from IIITH-mess-MCP: an HTTP GET to
`/api/registrations?from=YYYY-MM-DD&to=YYYY-MM-DD`, with the API key in the
`Authorization` header. No Node server is needed for this integration.

Save your key on the account page, or with `messwa user mess-key NAME`.
`MESS_API_URL` defaults to `https://mess.iiit.ac.in/api` and must be an HTTPS
base URL. Do not put keys in `config.toml` or commit them. Reading a stored
key needs `MESSWA_SECRET_KEY`; for Docker Compose it's in the ignored `.env`
file, which the existing `env_file` passes to the container. The standalone
binary uses its process environment and does not load `.env`.

```sh
./messwa registrations -from 2026-11-04 -to 2026-11-10
./messwa registrations -user asha    # with asha's key; the default is the first admin
./messwa bill                        # this month's bill: availed, projected and total
./messwa bill -month 2026-10 -user asha
```

With no dates, this reads today in the configured timezone. If only `-from`
is supplied, it reads that date. A range must be ordered and at most two months.
This command makes no WhatsApp connection and can run while the service is up.
The read-only `my_registrations` MCP tool accepts explicit `from` and `to` dates.

Output contains only meal date, meal, mess, category, cancellation and usage
timestamps. Account IDs and unrelated portal fields are discarded. Cancelled
or used meals remain visible; fetching a registration does not mark it for sale.
Every call reads live; no inventory cache or raw portal response is saved.
An unavailable portal or invalid response returns an error, not an empty list.
API keys are not forwarded on redirects or included in error messages.

The server must be connected to the IIIT network or VPN to reach the portal.
Registration reads do not register, cancel or skip meals.

### Fetch the current portal QR

```sh
./messwa fetch-qr                          # writes state/current-qr.png
./messwa fetch-qr -out state/meal-qr.png    # choose a private output path
./messwa fetch-qr -user asha                # with asha's key, not the first admin's
```

This reads `/api/auth/me` and encodes its current `token` directly, matching
the portal frontend's QR component. It does not reset the token or change the
daily-reset preference. The output file is replaced via a private temporary
file; the CLI does not print the token. Keep real QR images in the ignored
state directory. The `my_qr` MCP tool returns the PNG inline without saving it.
The image itself grants meal access and must be treated as sensitive.

When the portal resets the token, previously saved or sent QR images expire.
Fetch again afterwards. For delivery, use the live mode below to avoid relying
on a saved file. This integration does not schedule resets or revoke a QR for
one particular buyer.

## Setup

Needs Go 1.27+. On this server it's at `~/.local/go` (`export PATH=$HOME/.local/go/bin:$PATH`), the project lives in `~/messwa`, and Docker runs it (see below). The `messwa` wrapper in `~/.local/bin` runs the local binary from the project directory, which is fine for `dashboard -at` and `inspect` but not for `login`, `groups` or `run` while the container is up.

```sh
go build -o messwa .
cp config.example.toml config.toml

./messwa user add -admin <you>       # create your dashboard account first
./messwa login --user <you>          # scan a QR; --phone NUMBER uses a pairing code
./messwa groups                      # copy the mess groups' JIDs into config.toml
./messwa user add asha               # another person links their own WhatsApp on /account.html
./messwa run                         # serves http://127.0.0.1:8766/mcp
```

Stop `run` before using `groups`. Only one connection per linked device works at a time.

Add it to Claude Code:

```sh
claude mcp add --transport http messwa http://127.0.0.1:8766/mcp
```

### Docker

On this server the container is the live service. It uses host networking, so `config.toml` works unchanged: the dashboard binds the host's Tailscale address and MCP stays on loopback, where tokenless requests act as the first admin. If that address isn't up yet at boot, the bind fails and Docker restarts the container until it is.

```sh
docker compose up -d --build        # rebuild and redeploy after code changes
docker compose logs -f
docker compose stop                 # before `login` or `groups`
docker compose run --rm messwa groups
```

To serve MCP beyond loopback, set `[mcp] listen` to that address. Every request then needs a token: each person makes their own on the account page and passes `--header "Authorization: Bearer mwa_..."` to `claude mcp add`. Tokenless access only works on loopback.

The old systemd unit (`~/.config/systemd/user/messwa.service`) is disabled. Don't run both: WhatsApp allows one connection per linked device.

`/healthz` reports service availability. A disconnected user does not make the shared service unhealthy; the account page reports that user's connection status.

### Log files

Besides stderr (`docker compose logs`), messwa writes the same lines to
`<state_dir>/logs/messwa-YYYY-MM-DD.log`, one file per UTC day, matching the
UTC timestamps on each line. A restart appends to that day's file, so a test
run's log survives container and agent restarts with nothing tailing
`docker logs`. messwa keeps the newest 14 files and deletes older ones when
it starts a new day's file. Every command writes there, not only `run`. If
the directory can't be opened, messwa logs to stderr only and says so.

For what a sell task did, `sell_task_events` (MCP) returns its events in a
time window to the second, from the database, so it has every run's events
even when the log was rotated away.

### whatsmeow's log

whatsmeow, the WhatsApp library, logs to the same output as messwa, tagged
`component=whatsmeow` and its module. By default only warnings and errors
show. Set `MESSWA_WA_LOG` to `info` or `debug` for more (in `.env` for Docker,
then `docker compose up -d`). `debug` also logs every stanza sent and
received, which is a lot. Stanzas are cut down to their tags and attributes
first: chat JIDs, message IDs, push names and group settings appear, message
bodies never do, encrypted or not.

## Known limits

- History is mostly live-only. The first link gets a partial history sync (messages from the last 48h are ingested). After that, anything that arrives while messwa is down is lost.
- A sale agreed in DMs, where the seller never reacts or replies, still shows as available.
- Group members who appear only by LID (no phone number known) show a `jid` ending in `@lid` and no `phone`.
- whatsmeow is unofficial. A read-only device is low risk, but a ban is possible.

## Next: selling (phase 2)

### Group offer test

With the linked session free, run a single offer in a test group:

```sh
./messwa offer-group -group 'Test group' \
  -offer-text 'Test offer. Use Reply privately and type interested.' \
  -reply-text 'Thanks, I got your reply. What price were you thinking?'
```

The group name must match exactly. Use its JID if names are duplicated. The
command waits up to ten minutes for a private text reply that quotes its post,
sends the configured DM response once and exits.

### Group offer with payment and QR delivery

Use `deliver-qr -group` to post an offer and finish the sale in the buyer's DM
on the same connection. Set the fixed price with `-amount`. [Offer
settings](#offer-settings) post to several groups, hide the price, or list
extras.

```sh
./messwa deliver-qr -date 2026-11-04 -meal lunch \
  -group 'Test group' -amount 1.00 \
  -offer-text 'Test lunch offer for INR 1.00. Use Reply privately and wait for a DM before paying.'
```

The first buyer must reply privately to the quoted post within ten minutes
(or DM about it, with [plain DMs](#offer-settings) on). With
[reposts](#offer-settings) on, the ten minutes run from the last copy.
Later buyers who quote the same offer join the queue in arrival order. Only
the current buyer gets payment instructions (`ok 1`, then `pay using whatsapp pay pls, not gpay/upi`,
then `ill send the qr right after`). That buyer has up to two minutes
to pay, or can drop out with `not interested`, `nvm` and the like. The bot finishes checking
any payment it received on time before it moves on to the next buyer. A
payment from a previous buyer cannot release the QR after their turn ends.
Once the QR is sent, the bot privately replies `sold sorry` to everyone still
in the queue. See [What buyers see](#what-buyers-see) for every message. The queue also closes when the meal ends. The example sends the
embedded placeholder image.

For a live portal sale, pass `-portal-qr` and omit `-offer-text` to build the
group offer from the selected account's actual mess and diet.

```sh
./messwa deliver-qr -date YYYY-MM-DD -meal lunch \
  -group 'Test group' -amount 1.00 -portal-qr -user YOUR_ACCOUNT
```

The command uses the same live portal API as IIITH-mess-MCP. It checks the
registration before posting, checks it again before requesting payment, and
fetches the current QR after a matching payment message.

#### Offer settings

The `[sell]` section of `config.toml` sets where the offer goes and what it
says. Each setting has a flag of the same name that overrides it for one run.
The defaults post to one group, price included, no extras:
`sell kadamba veg lunch 30`, with `(tmrw)` or `(13 nov)` for a meal that
isn't today's.

| Setting | Flag | Default | Does |
| --- | --- | --- | --- |
| `groups` | `-group`, repeatable | none | Groups to post to, by exact name or JID, in order |
| `group_count` | `-group-count` | 0 | Post to the first N of the top-level `groups` instead. Set this or `groups`, not both |
| `post_gap` | `-post-gap` | `15s` | Pause between copies, at least 3s |
| `show_price` | `-show-price` | `true` | Put the price in the group post |
| `mention_extras` | `-mention-extras` | `false` | List the extras registered for that meal |
| `handoff` | `-handoff` | `never` | When to hand the buyer's chat to you, see below |
| `accept_plain_dms` | `-accept-plain-dms` | `false` | Let DMs that ask about the offer without quoting it join the queue, see below |
| `buyers` | `-buyers` | `anyone` | Who may buy. `unsaved_only` skips saved contacts, see below |
| `repost` | `-repost` | `true` | Post a fresh copy in a group once the last one has dropped out of view, see below |
| `repost_after` | `-repost-after` | `10m` | Repost once a copy is this old. `0s` turns it off |
| `repost_messages` | `-repost-messages` | `10` | Repost once this many messages came in after a copy. 0 turns it off |
| `repost_listings` | `-repost-listings` | `3` | Repost once this many other sellers listed the same meal at the same mess after a copy. 0 turns it off |
| `max_reposts` | `-max-reposts` | `6` | Most fresh copies per group for one offer, 0 to 12 |
| `accept_payment_images` | `-accept-payment-images` | `false` | Count an image from the paying buyer as their payment, unverified, see below |

With `groups` or `group_count` set, `deliver-qr` without `-peer` posts to
those groups, so `-group` is no longer needed:

```sh
./messwa deliver-qr -date YYYY-MM-DD -meal lunch -amount 60.00 -portal-qr
./messwa deliver-qr -date YYYY-MM-DD -meal lunch -amount 60.00 -portal-qr \
  -group-count 1 -show-price=false -mention-extras
```

With `show_price = false` and `mention_extras = true` a Kadamba lunch goes out
as `sell kadamba non-veg lunch + extras: Chicken 65`. The payment request DM
always states the price. Settings only shape the offer built with
`-portal-qr`; `-offer-text` is posted exactly as written.

**Several groups.** Every copy feeds one buyer queue. The first private reply
to any copy reserves the meal, later replies to any copy queue behind it, and
a buyer who replies to two copies gets one turn. The delivery ledger still
holds one reservation per date and meal, so only one QR goes out. The sale
starts as soon as the first copy is posted and the rest follow `post_gap`
apart. Once the meal sells or is handed off, no more copies go out. If one copy fails to post,
the others still go out and replies to the failed one still count, in case
WhatsApp posted it anyway. The command stops only when no copy posts.

Right before each copy, messwa asks WhatsApp for that group's details
again. whatsmeow builds a group message from the details it cached last,
and the group list it reads first can describe a group only partly. On
12 Nov 2026 the list returned "Mess Buy Sell @ IIITH - 2", a community
subgroup, with no name, and every post to it failed with `server returned
error 479` (invalid stanza). A partial cache entry is the suspected cause,
not a confirmed one. Each failed copy is logged as it fails, with the
group's name (or its JID when WhatsApp gives none) and the addressing mode
and member count from both the list and the fresh query. The list gives
IIITH - 2 no addressing mode and no members every time it's asked, so the
"described this group differently" line shows at info once per group, and
again only if the fresh details change; later posts log it at debug. If posts to a
group keep failing, rerun with `MESSWA_WA_LOG=debug` (see
[whatsmeow's log](#whatsmeows-log)).

**Reposts.** In these groups a post scrolls out of view within minutes at
lunch and dinner. While the meal is unsold, messwa puts a fresh copy in a
group when the first of these happens there:

- the latest copy is `repost_after` old (10 minutes);
- `repost_messages` posts (10) came in after it, about a phone screen of
  short posts;
- `repost_listings` other people (3) listed the same meal at the same mess
  after it. A listing tagged with a different diet doesn't count, an
  untagged one does, and one person counts once however often they post.

The counts come from the group log, the same posts the board is built
from, tagged by the same mess and meal rules. messwa writes its own posts
into the log when it sends them, so "after ours" means after that row:
later by timestamp, or the same second and logged later. History that
syncs late still lands in timestamp order. A group that isn't one of the
top-level `groups` has no log, so only the time trigger works there.

The defaults come from 11 days of the four IIITH groups. In the busiest
two, a sell post in the last four hours before a meal ends is followed by
a median of 8 to 11 messages and 2 to 3 same-mess listings within 10
minutes, so those groups often repost after 6 to 9 minutes. The quietest
gets about 2 messages in 10 minutes and reposts on the timer.

Limits that hold whatever the settings say:

- A group gets no copy sooner than 5 minutes after the last one there, and
  at most `max_reposts` fresh copies per offer.
- No reposts in the meal's last 15 minutes. About 2.5% of these groups'
  sales happen in the last 10 minutes, too late to haggle and pay.
- A group whose copy failed to send gets no more copies from that offer, so
  a group that always refuses (WhatsApp error 479, say) costs one send.
- Reposting stops when the meal sells, is handed off, the task is
  cancelled or the meal ends.

Copies stay up. A reply to any copy, old or new, joins the same buyer
queue, and after a sale every copy gets the ✅. Reposting doesn't delete
the copy before it.

With `repost` on, `deliver-qr -group` waits for a first reply until ten
minutes after its last copy, once no group can get another, instead of ten
minutes after the first.
**Plain DMs.** By default only a private reply that quotes a copy of the
offer counts. Plenty of buyers just DM "price?" or "is the kadamba lunch
available" instead. With `accept_plain_dms = true` (or `-accept-plain-dms`)
such a DM joins the same queue, after anyone already in it, if:

- it's a new text DM sent after the first copy was posted, not an edit, and
  doesn't quote some other message;
- it asks about this offer. It names the offer's mess, or it asks the price
  or whether the meal is available and either names the meal or is at most
  8 words. A DM that names only another mess or another meal, or says "not
  interested", doesn't count;
- you haven't sent that person a DM in the 30 minutes before it, or since.
  messwa reads this from the DM times `run` records, plus anything you send
  from your phone while the offer is up. This keeps the bot out of a chat
  you're already having.

The buyer then gets the usual payment request and the same two minutes,
and only a matching WhatsApp Pay payment from them releases the QR.
Everything else works as for quoted replies: one turn per person, the floor
price, and for sell tasks the send limits. With `handoff = "on_reply"` the
first such DM hands the chat to you.

The risk is that the bot answers someone who wasn't asking about the meal,
such as a friend who opens with "how much?" about something else. They get
one payment request they can ignore. The 30-minute rule only catches chats
you've been in recently, so if that worries you, leave the setting off.

**Extras.** The extras come from the portal's `/registrations/extras`, the
same read as IIITH-mess-MCP's `get_extra_registrations`. Extras already eaten
or registered at another mess are left out. If the read fails, the offer goes
out without extras and the log says why.

**Saved contacts.** By default anyone who replies privately can buy,
friends included. With `buyers = "unsaved_only"`, a reply from someone in
your address book never joins the buyer queue: the bot logs `a saved contact
replied to the offer; skipped them and sent nothing` and sends them nothing,
not a payment request, not `sorry sold`, not a handoff. "Saved" is the same
check that decides whether a DM's text is stored (see [What gets
stored](#what-gets-stored)): a name in your address book, looked up under
both the buyer's number and their LID, so a buyer who appears only by LID
still matches a contact saved under their number. `-buyers` doesn't apply to
`-peer`, where you picked the buyer yourself.

**Payment images.** Some buyers pay by UPI outside WhatsApp Pay and send a
screenshot. The bot can't read those, so by default the image doesn't count.
The buyer hears `cant check screenshots, pay using whatsapp pay pls` once,
and their two minutes keep running. With `accept_payment_images = true` (or
`-accept-payment-images`), an image counts as the payment if:

- it's an image message, in a DM from the buyer whose payment turn it is;
- it arrives after the bot sent them the payment prompt, so after they
  agreed a price;
- it's inside their pay window: their two minutes, or until the meal ends
  for `-peer`.

Then the sale goes on as after a WhatsApp Pay payment: the QR goes out, the
others hear `sold sorry` and the offer posts get a ✅. The log warns that
the payment was assumed from an image, the delivery ledger records it as
paid by image, the sales page shows "By image, not verified" and SOLD ·
UNVERIFIED, and a sell task ends with a detail that says so.

**A screenshot proves nothing.** Anyone can send an old screenshot, someone
else's, an edited one, or a picture of their lunch, and the bot sends the QR
all the same. A sent QR can't be taken back. Turn this on only if you check
your UPI app after every such sale and can live with losing a meal now and
then. Images from other buyers, images sent before the prompt and anything
after the window never count, and every image the buyer sends during their
turn that doesn't count is logged with the reason.

With `-peer`, the setting and the flag apply too. There's no prompt unless
you pass `-offer-text`, so any image from the buyer after arming counts:
`-peer` means you already agreed the price in the chat.

### Taking over the chat yourself

The selling plan has the bot hand the chat to you so you can negotiate. Set
`handoff` in `[sell]` in `config.toml`, or pass `-handoff` to override it for
one run:

| `handoff` | What the bot does when a buyer replies privately |
| --- | --- |
| `never` (default) | Asks them to pay the `-amount` and sends the QR when they do |
| `on_reply` | Sends them nothing and hands the chat to you |

```sh
./messwa deliver-qr -date 2026-11-04 -meal lunch \
  -group 'Test group' -amount 60.00 -portal-qr -handoff on_reply
```

With `on_reply`, the first private reply to any copy ends the command and
stops further copies. It prints the buyer's name and number, a `wa.me` link
to their chat, and anyone else who had replied by then, and logs the same. The bot stops listening, so later replies
reach only your phone. The ledger records the meal as handed off, and the
sales page shows it with a HANDOFF stamp and the `-amount` the offer asked.

Finish the sale one of two ways:

- Agree a price in the chat, then let the bot collect it and send the QR.
  The command to run is in the handoff output:

  ```sh
  ./messwa deliver-qr -date 2026-11-04 -meal lunch \
    -peer '91XXXXXXXXXX@s.whatsapp.net' -amount 50.00 -portal-qr
  ```

  This is the one-meal delivery below. It takes over the handed-off meal with
  whichever buyer and price you give, so you can also sell to someone else
  who replied. The sale then counts on the sales page like any other.
- If the sale falls through, free the meal so a new `deliver-qr -group` can
  offer it:

  ```sh
  ./messwa release-handoff -date 2026-11-04 -meal lunch
  ```

  This changes only the database, so it's safe while `run` is up. It refuses
  meals that are armed, paid or sent. For an armed meal nobody paid for, add
  `-unpaid` (see [One-meal QR delivery](#one-meal-qr-delivery)).

If you send the QR from your phone instead, messwa never sees the payment.
The meal stays handed off on the sales page and doesn't count toward
earnings. If the portal shows you ate it, the page shows ATE.

### Marking the meal sold

`deliver-qr -group` records each offer post's group and message ID in the
data database, one row per copy as it posts, reposts included. Once a QR reaches the `sent` state, the command reacts ✅ to
every offer post recorded for that date and meal, in any group and from any
earlier `deliver-qr -group` run. This also happens after a `-peer` delivery
for a meal that was offered in a group first. A reaction that fails is logged
as a warning and the sale still counts.

If you finish a sale yourself, for example after taking over the buyer's
chat or sending the QR from your phone, record it with `mark-sold`:

```sh
./messwa mark-sold -date 2026-11-12 -meal lunch \
  -peer '+91 98765 43210' -name Daniel -amount 20 -paid-by image -at 13:45
```

It writes the meal's row in the delivery ledger as `sent`, so the sales page
counts the sale. Without that row the page falls back to the portal, sees the
meal scanned and shows ATE.

- `-peer` is the buyer's number with its country code, or their WhatsApp JID.
  `-name` is optional; a handed-off sale keeps the name it already has.
- `-amount` is what they paid, like `20` or `20.50`.
- `-paid-by` is `whatsapp` (the default), `image` for a UPI screenshot,
  which the sales page shows as SOLD · UNVERIFIED, or `other` for cash or
  anything else.
- `-at` is when the QR went out, `HH:MM` on `-date` or an RFC 3339 time. It
  defaults to now.
- The mess and diet come from the meal's registration, read with the first
  admin's mess API key (`-user NAME` picks another). If the portal can't be
  read, the sale is still recorded without them.

A meal with no row gets one. A handed-off or armed row becomes `sent` with
these details. A meal that is already `sent` or `sending` is left alone with
an error; check the row and pass `-force` to overwrite it. A meal an active
sell task holds is refused; cancel the task first.

Then it reacts ✅ to the meal's recorded offer posts, which needs the
WhatsApp session, so stop `run` first, like the other sending commands. With
no recorded posts it records the sale and stops there; posts you made from
your phone aren't recorded, so react to those from the phone. Pass
`-react=false` to only record the sale: that touches only the database and is
safe while `run` is up. Running it again with `-force` sends the ✅ again,
which also restores one you removed. It exits with an error if any reaction
failed, after trying the rest.

WhatsApp doesn't deliver a message back to the device that sent it, so `run`
never sees an offer or ✅ that messwa sent. `deliver-qr` and `mark-sold`
write both into the group log themselves, as posts from the linked account,
for groups listed in `config.toml`. The board then shows the offer as your
listing, and the ✅ marks it sold like any seller's tick. A ✅ you add or
remove from your phone arrives as a normal event. The sales page shows the
sale from the ledger row `mark-sold` records.

### One-meal QR delivery

Stop `run` or `inspect-payment` before using the same linked session. Arm one
date/meal for a specific buyer and agreed INR amount with two decimal places:

```sh
./messwa deliver-qr -date 2026-11-04 -meal lunch \
  -peer '91XXXXXXXXXX@s.whatsapp.net' -amount 2.37
```

Without `-qr`, this sends an embedded image labelled **TEST IMAGE / NOT A MEAL
QR**. It works in the Docker image too. For a real sale, pass
`-qr state/meal-qr.png`; keep your real QR in the ignored state directory.
The image must be PNG or JPEG, no larger than 5 MiB or 8192 pixels per side.
The date is interpreted in the configured timezone and the configured meal's
end time is the delivery deadline. In `-peer` mode, agree the price directly
with the buyer before arming.

Optionally pass `-offer-text "Selling Palash breakfast for INR 2.37. Pay through
WhatsApp Pay here and I will send the QR."` to send that text to the selected
buyer after arming and connecting. It is sent once per invocation; omit the
flag when resuming if you do not want to repeat the offer.

For the sales page, `deliver-qr` also records when the offer went out, when
the buyer paid, the name on their payment message and when the QR was sent.
With `-portal-qr` it records the meal's mess and diet from the registration.

To deliver the current real portal QR instead of a saved image:

```sh
./messwa deliver-qr -date 2026-11-04 -meal lunch \
  -peer '91XXXXXXXXXX@s.whatsapp.net' -amount 60.00 -portal-qr
```

`-portal-qr` reads with the first admin's saved mess API key (`-user NAME` picks another account) and cannot be combined with `-qr`. It
checks for exactly one regular, uncancelled, unused registration before arming.
After a matching payment, it rechecks that meal and fetches the current QR
immediately before uploading. A portal error blocks sending; it never falls
back to a saved or placeholder image. The delivery binds to a hash of the
account ID, so daily token rotation does not prevent resuming an armed slot,
while switching portal accounts does. Keep separate keys and state directories
for separate people. The portal can still invalidate an image after it is sent.

Delivery requires a new incoming WhatsApp Pay `sendPaymentMessage` from the
selected buyer, after arming and before the meal ends. If the amount is
missing, the bot requests the full message from the primary device, up to six
times within 40 seconds. If the amount is still unreadable after that, it
logs a warning and sends nothing. In a group sale the buyer's two minutes then
run out as usual. Check the buyer's chat and deliver by hand if they paid.

`-assume-unreadable-payment` changes that: the bot assumes the agreed amount
and sends the QR if the sale is still open, and logs the assumption with the
triggering message ID. Anyone who can make WhatsApp deliver a payment message
without a readable amount then gets the QR without paying, so leave it off
unless you check payments yourself afterwards.

Normal text, screenshots (unless `accept_payment_images` is on, see [Offer
settings](#offer-settings)), other buyers, expired messages, mismatched
payment details and known incomplete transactions do not qualify. The WhatsApp event
does not independently verify that funds arrived.

When the buyer's WhatsApp Pay payment doesn't qualify, the log says so at
info level with the payment's message ID and which check failed, such as
`payment amount or currency does not match`. It never logs the payment's
contents. `-diagnostics` adds the same line for the buyer's other messages.

The data database stores the buyer, amount, image hash or portal-account binding, arming time, payment
message ID, outgoing image ID and state for each date/meal, plus the group
and message ID of each offer post and when messwa reacted to it. It never stores
the raw payment payload. Repeated events and restarts cannot automatically
send the same slot twice. An armed slot can resume only with the same buyer,
price and image. A payment message cannot claim another meal.

Network failures or a crash after claiming leave the delivery as `sending`.
The command refuses to rearm `sending` or `sent` slots. Check the buyer's chat
and make any follow-up manually; a timeout does not prove that delivery failed.
The `sent` state records WhatsApp's send acknowledgement, not the buyer opening
the image. Keep the same state database when restarting. Do not run two
connections using the same linked-device session.

A `-peer` run you stop before the buyer pays leaves the meal `armed` for that
buyer at that price. Rerunning with the same `-peer`, `-amount` and image
picks it up again. Any other buyer or price is refused until you free it:

```sh
./messwa release-handoff -date 2026-11-12 -meal lunch -unpaid
```

`-unpaid` frees a meal that is `armed` with no payment claimed, as well as a
handed-off one. It refuses meals that are paid, `sending` or `sent`, and
meals an active sell task is selling (cancel the task, which frees its
reservation). It changes only the database, so it's safe while `run` is up.
Stop the `deliver-qr` that armed the meal first: if that buyer pays after the
release, that run finds no reservation and sends no QR.

This command is not exposed through MCP and does not implement automatic
registration imports, scheduling, negotiation or refunds. To negotiate,
use `-handoff on_reply` (see [Taking over the chat yourself](#taking-over-the-chat-yourself)),
or a sell task, which negotiates by itself.
Sell tasks use the same sale code from inside `run`; see below.

### Payment diagnostic

`inspect-payment` listens to one explicitly selected DM, including a saved
contact when selected. Stop `run` before reusing its session.

```sh
go build -o messwa .
# First setup only: copy config.example.toml to config.toml and create a user and run messwa login --user NAME.
./messwa inspect-payment -peer '91XXXXXXXXXX@s.whatsapp.net'
```

Use the test sender's number with country code, or their full `@lid` JID.
Only new incoming messages from that peer are processed. Normal messages
produce field names only. A `sendPaymentMessage` can produce `decoded_prefix`
with currency, decimal amount, timestamp and an optional note. Amounts remain
strings to avoid floating-point rounding. The experimental decoder recognizes
the observed Android UPI Parcel layout, including `interopNote` when the
separate `noteMessage` is empty. Other layouts may not decode.

Header codes remain unmapped. `completion_status` is `unknown` and
`bank_verified` is always false. These are sender-provided message details,
not independent confirmation of a bank credit. No automatic sale or QR release
is implemented. The diagnostic does not attach the ingester, store DM content
in the data database, send chat messages, or expose extra MCP tools.

`-raw` includes raw payment fields in stdout for local debugging. It can expose
account details. Default output omits those fields but includes the decoded
amount and note. Keep output and session files private. Ctrl+C stops listening.

Validation used real incoming payments for INR 1.00, 3.00, 2.37 and 1.68, with
a note in the final sample. Fixtures contain only a non-identifying prefix and
synthetic note data. Tests cover malformed/truncated payloads, unknown note
wrappers, duplicate note keys and Unicode. No Java objects are deserialized.

### Sell tasks

Sell tasks are steps 1 to 6 of the selling plan in `instructions/selling-instructions.md`: you tell your agent "sell my lunch for between 30 and 60" and it calls `start_sell_task`. The task runs inside `run`, on its WhatsApp connection, so the board and the rest of MCP keep working during a sale. It uses the same code as `deliver-qr -group -portal-qr`: the registration check, the group posts, the buyer queue, payment matching, the QR delivery and the ✅ on the posts.

Turn them on with the switch on the settings page's OFFER tab, or in `config.toml` and restart `run`:

```toml
[mcp]
sell_tasks = true
```

What a task does:

1. Checks your portal registration for that meal with your saved mess API key, the same way `deliver-qr -portal-qr` does.
2. Posts the offer using the `[sell]` settings, like `deliver-qr` does with no flags (see [Offer settings](#offer-settings)): `groups` or `group_count`, `post_gap` between copies, `show_price`, `mention_extras` and `accept_plain_dms`. With `show_price` the post shows `max_price`, e.g. `sell bakul non-veg lunch 60`. If `[sell]` names no groups, the task posts in the first group in `groups`. A group that can't be found is skipped; the task fails only if no copy posts. Until the meal sells, it reposts in each group under the `repost` settings (see [Reposts](#offer-settings)).
3. Takes everyone who replies privately to any copy, in reply order. A plain DM that doesn't quote a post counts only with `[sell] accept_plain_dms = true`, under the rules in [Offer settings](#offer-settings). With `[sell] buyers = "unsaved_only"`, saved contacts are skipped and get nothing (see [Saved contacts](#offer-settings)).
4. With `max_price` above `min_price`, negotiates with each of them at once (see [Negotiation](#negotiation)). Buyers who agree a price queue to pay, highest price first and in the order they agreed among equal prices. With the two prices equal, every buyer queues to pay that price.
5. Asks the first buyer in the pay queue to pay their price within two minutes. They can drop out with `not interested`, `nvm` and the like. Then the next buyer gets a turn, as in `deliver-qr -group`.
6. Sends the QR after a matching WhatsApp Pay payment (or, with `[sell] accept_payment_images = true`, an unverified image from that buyer after the prompt; see [Payment images](#offer-settings)), stops posting copies, tells everyone else who replied `sold sorry`, reacts ✅ to the meal's offer posts, and ends as `sold`.

`sell_task_status` lists every copy with its copy number (0 for the first in a group, then one per repost), marks each group's current copy, and under `reposts` says per group how many messages and same-mess listings came in after the current copy, when the next copy goes out and why, or why that group gets no more.

`[sell] handoff` doesn't apply to tasks. Instead `start_sell_task` takes `handoff`, `never` by default:

| `handoff` | What the task does |
| --- | --- |
| `never` (default) | Negotiates and closes the sale: payment, QR, ✅ |
| `on_reply` | Steps 4 and 5 don't happen: the first private reply ends the task and the bot sends that buyer nothing |
| `on_agreement` | Negotiates and hands you the first buyer to agree a price; the bot doesn't ask them to pay, and keeps selling until you mark the meal sold |

With `on_reply` the copies stop and the task ends as `handed_off`. With `on_agreement` the task stays active: the copies keep going up, the bot keeps haggling with everyone else, and buyers who agree no more than the handed-off buyer hear `ok 40`, `someone else is buying rn, ill msg u if it falls through`. A buyer who agrees more takes the handoff: the bot tells the first buyer `wait dont pay yet`, `someone offered more, ill msg u if it falls through`, puts them back in line at their price, and hands you the new buyer. There is no time limit on a handoff, and the bot can't see a payment made to you outside WhatsApp Pay, so mark the meal sold (dashboard or `mark_sell_task_sold`) as soon as the buyer pays, or a higher offer can still take it. If the meal ends first, the task ends as `handed_off`. In both modes the meal's delivery record is handed off, as with `deliver-qr -handoff on_reply`, so the sales page shows HANDOFF. `sell_task_status` returns the buyer's name, number and `wa.me` link, the price asked (or agreed, with `on_agreement`), who else replied, and the `deliver-qr -peer` and `release-handoff` commands to finish or drop the sale.

A task also ends when you cancel it (`cancelled`), when the meal ends (`expired`), or on an error (`failed`, with the reason in `detail`). Ending without a sale releases the unpaid reservation, so you can start a new task for that meal. One meal can have only one active task, and none once it has a delivery record.

Cancelling deletes the task's group posts for everyone, unless you pass `keep_posts`. A post still being sent when you cancel gets to finish (or time out after a minute) and is then deleted with the rest. The bot only deletes copies the task claimed, so never anyone else's message. A copy that never went out, one older than 48 hours, or one WhatsApp refuses to delete stays up: `sell_task_status` shows its `revoke_error`, and the log says to delete it by hand. Sold posts keep their ✅, and expired tasks keep their posts.

Until the meal ends, a private reply to a cancelled task's post goes to another active task selling the same meal at the same mess and diet (`dinner#2` counts as `dinner`), as if the buyer had replied to that task's post. With two such tasks, the older one takes it. With none, the reply gets nothing, the same as a reply to a post whose meal already sold. This holds across a restart.

`run` enforces these rules on every message, so the agent can't get around them:

- Nothing is sent unless a task is active. The one exception is a QR for a payment that came in while the task was active. It still goes out if you cancel a moment later, and the task then ends as `sold`.
- Besides one claimed post per group, a task messages only people its posts queued: those who replied privately to one, to a cancelled task's post for the same meal, or with `accept_plain_dms`, DMed about it. After the QR goes out it may also react ✅ to the meal's recorded offer posts, and to nothing else. After a cancel it may delete its own posts.
- With `[sell] buyers = "unsaved_only"`, a task never queues or messages a saved contact. If you switch to it while a task is running, the restart skips saved contacts still waiting in the queue. A buyer already asked to pay keeps their turn, and gets the QR if they pay.
- No buyer is quoted, agreed with or asked for less than `min_price`. The store checks this itself on every quote, agreement, reservation and sale, whatever the negotiation decided. The one exception is a price the bot already quoted or agreed with that buyer before `set_sell_task_prices` raised the floor: that price stands. A payment must match exactly the price agreed with that buyer, or a higher one the bot quoted them.
- A task sends at most 12 messages a minute and 80 in total. Reposts count toward both, and a task stops reposting when it has 20 sends left, keeping those for buyers. All tasks together send at most 15 a minute. One buyer gets at most 6 negotiation messages. The ✅ reactions after a sale skip the per-minute wait, so they don't time out behind the sale's own sends.
- A task stops at the meal's end time.

Each send is written to the database before it goes out: each group post, each negotiation message, each payment prompt, each `sold sorry`, and each once-only line (`only whatsapp pay sorry`, the screenshot reply). The QR goes through the existing delivery ledger. When `run` restarts, it resumes active tasks before reconnecting and never repeats a claimed send. Replies to copies from before the restart still count, and groups that had no copy yet get one, `post_gap` apart. Each repost is claimed with its copy number before it goes out, so a restart never posts the same repost twice, and the next one is due from the last copy's time, not from the restart. A buyer whose turn was under way keeps it, with at least 30 seconds after the restart, so a payment sent while `run` was down still counts. Buyers who were negotiating carry on from the bot's last price, and buyers who had agreed keep their place in the pay queue. A post or QR send that was claimed but never confirmed is not retried. A task with an unconfirmed QR send ends as `failed`; check the buyer's chat. Turning `sell_tasks` off cancels active tasks on the next start.

#### Changing prices

`set_sell_task_prices` takes a task id, `min_price` and `max_price` and changes them while the task runs. The posts stay up, nobody is messaged, and every buyer keeps their place. Use it instead of cancelling and starting a new task, which reposts in every group and drops every haggle.

- Buyers mid-haggle get the new range on their next message. The target (the meal's usual price, clamped to the range) is worked out again, and `sell_task_status` shows it as `target_price`.
- A price the bot already said stays said. A raised floor stops the bot going lower than a buyer's current ask but never raises it, and a buyer who agreed below the new floor still gets their turn at that price. A lower `max_price` lowers asks above it.
- New buyers open at the new `max_price`, and reposts show it with `show_price`. A buyer who pays the old posted price still counts.
- When the floor drops, buyers who already left (timed out, passed, or parked) with a last offer at or above the new floor show under `needs_you`, saying what they offered. The bot doesn't message them. If you tell one to go ahead, a WhatsApp Pay payment of their offer from them gets the QR, as long as nobody holds the meal. A timed-out buyer who writes again is picked back up by the bot at the new range, and leaves `needs_you`.
- A negotiating task must keep `max_price` above `min_price`, and a fixed-price task must keep them equal. Cancel and start again to switch.
- The task must be active. The new prices are saved, so a restart keeps them. Each change goes in the task's event log, and `sell_task_status` lists them under `price_changes` with the old and new range.

#### Negotiation

Buyers message several sellers at once and leave within a minute, so the bot answers everyone who replies straight away, from fixed templates. It reads each reply with fixed rules (`negotiate_parse.go`), built from a study of a few hundred real mess trades in DMs. Buyer text can only pick one of a few meanings and, for an offer, one number; there is no LLM in the loop. Only the numbers are stored, never the text.

**Prices.** The bot opens at `max_price` with just the number: `60`. It never says the price is negotiable; buyers assume it is. It doesn't invite payment yet, because two buyers paying at once means a refund. It aims for the meal's usual price: the median ask for that serving at that mess, falling back to the same weekday's meal at that mess over four weeks (Wednesday and Sunday lunches go for more), then every mess, then the meal over four weeks. With no asks it aims for `max_price`. The lowest it accepts starts at that target, and over the last three hours before the meal ends it slides down to `min_price`. While two or more buyers are negotiating or waiting to pay, it stays at the target.

| The buyer says | The bot |
| --- | --- |
| `available?`, `price?`, `buy`, `interested`, `?` | Quotes the opening price |
| `40?`, `buy @50`, `rs 25`, `20-25` (the higher end), `sixty` | At or above its price: agrees to its price. At or above its lowest: agrees to theirs. Below: counters halfway, in ₹5 steps |
| `45 final`, `max 45`, `last 45` below its lowest | Says `50`, then `final offer` |
| `too much`, `negotiable?`, `pls`, `people are selling at 70` | Comes down one step |
| `ok`, `fine`, `done` (after a price), `same number?`, `gpay?`, `paying` | Agrees to its current price |
| `nv?`, `kadamba?` | `bakul non-veg, 50` |
| `upi id?`, `send qr`, `cash?`, `can i pay in other ways` | `only whatsapp pay sorry`, once. Asking again hands them to you |
| `nvm`, `sorry bought`, `got from other`, `no thanks` | Counters once with a lower price. A second pass, or a pass after its lowest or at `min_price`, stops talking to them |

After two counters the next one is its lowest. A buyer still below that after the bot says so is handed to you.

**A buyer who passes** gets one lower price before the bot lets them go. Above the target, that's the target. At or below the target, it's halfway from the current price to `min_price` in ₹5 steps, at least one step down: `70` with a floor of 60 becomes `how about 65`. This can go under the lowest the bot would otherwise take right now, since the buyer is leaving anyway; `min_price` still holds. A buyer who has heard `final offer`, or whose price is already `min_price`, ends as `passed` with no counter. The counter shows in the task's events as `pass_counter`. A pass that names a price (`too much, 40?`) is an offer and goes through the offer rules instead.

**Agreeing and paying.** A buyer who agrees joins the pay queue. If nobody is paying, they get the usual payment prompt at once, at the agreed price. If someone is paying at the same price or more, they hear `ok 50`, then `someone else is paying rn, if they dont in 2 min its yours`, and get the prompt when their turn comes. The bot keeps haggling with everyone else while one buyer has the turn. A buyer who agrees more than the one paying takes the turn: the bot tells the first buyer `wait dont pay yet`, `someone offered more, if they dont pay in 2 min ill msg u`, and asks the new buyer to pay at once. The first buyer keeps their price and their place in the queue, and gets the prompt again if the meal frees up. A payment the bot is already handling, including one it's still recovering from the phone, keeps the turn. If the first buyer pays after being told not to, that payment shows in `needs_you` for a refund like any other payment outside a turn. A buyer who pays a price the bot quoted them before their turn gets the meal if nobody holds it. If someone does, that payment isn't honoured, and the buyer shows in `needs_you` with the amount to send back.

**Buyers who need you.** The bot stops talking to a buyer, without ending the task, when they send two messages it can't read in a row (it restates its price once first), stay below its lowest, ask to pay some other way a second time, say they paid but no WhatsApp Pay payment arrives within a minute, or reach the limit of 6 replies (a two-message reply counts once). `sell_task_status` lists them under `needs_you` with why, a `wa.me` link, the bot's last price and their last offer. To sell to one of them yourself, cancel the task and use `deliver-qr -peer`. Lowering the floor also lists buyers whose offer it now meets; see [Changing prices](#changing-prices).

A negotiation times out when the buyer has been quiet for `[sell] negotiation_silence` (10 minutes by default, 1 to 30) after the bot's last message, or after six minutes in all, or the silence if that's longer. At most 10 buyers negotiate at once; the rest wait their turn. Nothing is reserved for a quiet buyer, but they hold one of those 10 slots and count as competition, which slows the price drop.

A buyer who timed out and writes again while the task is active and the meal unsold negotiates again from where they stopped: the bot keeps its last ask, the prices it quoted, their last offer and its message count, so it doesn't open at the full price again and the 6-reply limit still holds. The event log records `revived`. Buyers who passed, were parked or skipped stay out. If the task has sold the meal, a timed-out buyer who writes before the meal ends hears `sold sorry` once. After a cancelled, expired or failed task the bot says nothing, since you may be selling the meal by hand.

#### What buyers see

Every DM the bot sends is in `buyer_text.go`. It writes like the sellers in the mess groups: lowercase, a bare number with no ₹, the meal named only in the group post, and a `?` to ask. Most replies have two to four variants picked at random, so a buyer who asks every meal doesn't get the same lines each time. Some go out as separate messages about a second apart, like `50` then `final offer`. The agreed price stays `ok {price}`, and the waiting message stays `someone else is paying rn, if they dont in 2 min its yours`. Payment instructions and the separate QR promise each pick their own variation; test mode sends a test QR notice instead of the promise.

| When | The bot says (one variant) |
| --- | --- |
| First price | `60`. For a meal that isn't today's: `60, its for tmrw` |
| Coming down | `how about 50`, then `45?` |
| Its lowest | `40`, `final offer` |
| `nv?`, `kadamba?` | `kadamba non-veg for 60` |
| A message it can't read | `60, want it?` |
| Wants to pay another way | `only whatsapp pay sorry` |
| Agreed while someone else pays | `ok 50`, `someone else is paying rn, if they dont in 2 min its yours` |
| Outbid during their payment turn | `wait dont pay yet`, `someone offered more, if they dont pay in 2 min ill msg u` |
| Payment turn | `ok 50`, `pay using whatsapp pay pls, not gpay/upi`, `ill send the qr right after` |
| An image during the turn, with `accept_payment_images` off | `cant check screenshots, pay using whatsapp pay pls` |
| The QR | captioned with the mess and meal, e.g. `kadamba lunch` |
| Sold to someone else | `sold sorry` |

The payment prompt names WhatsApp Pay and rules out GPay and UPI because a payment to the number never reaches the bot. It doesn't give a deadline; the two minutes still apply.
