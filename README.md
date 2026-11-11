# messwa

messwa links to WhatsApp as a companion device (via [whatsmeow](https://github.com/tulir/whatsmeow)), merges the mess buy/sell groups you pick into one deduplicated list of listings, and serves that list plus limited DM info to an agent over MCP.

The WhatsApp access limits live in the binary. Ingest drops every chat outside the allowed set before anything touches disk, and WhatsApp MCP tools read only that stored data. Optional portal tools separately read your own registrations and current QR, using the mess API key saved on your messwa account.

The default `run` service and MCP tools are read-only: no messages, read receipts or presence are sent. Your phone keeps showing messages as unread and notifications don't change. The separate, explicitly armed `deliver-qr` command can send one image after a matching payment message.

## What gets stored

| Source | Stored |
| --- | --- |
| The configured mess groups | Posts that look like a listing (a sell/buy phrase or a meal name), reactions, edits, deletes. Pruned after 14 days |
| Ended listings, kept for good | One row each in `listing_facts`: seller's number and name, mess, meal, date, diet, price, when it was posted and sold. No message text |
| DMs with saved contacts | Name and last-message times only |
| DMs with unsaved numbers | Name, times, and message text (deleted after `dms.retention`, default 72h) |
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

| | Admin | Member |
| --- | --- | --- |
| Board, market pulse, listing details | yes | yes |
| `sales.html` | the linked WhatsApp account's sales, buyers, regulars, earnings, plus their own registrations | their own registrations only: eaten, wasted, still open, and what the wasted meals would have fetched |
| Coins for armed and sold meals on the day track | yes | no |
| `insights.html`: market analytics, every seller's number and record | yes | no |
| MCP `my_registrations`, `my_qr` | with their own mess key | with their own mess key |
| MCP `list_recent_chats`, `read_unsaved_chat` | yes | no |

Everything tied to the one linked WhatsApp device belongs to admins: `deliver-qr`, the sales ledger, DMs and saved contacts. `login`, `groups` and `inspect-payment` are CLI-only and need shell access anyway. Make yourself the admin and everyone else a member.

Each person saves their own mess API key on the account page (`/account.html`, the ACCOUNT button on the board). The key never leaves the server again after that. The page also changes your password and creates and revokes your MCP tokens.

**Keys at rest.** Mess API keys are encrypted with AES-256-GCM. The key for that lives in `MESSWA_SECRET_KEY` (64 hex characters, `openssl rand -hex 32`) in `.env`, not in `state/`, so a copy of the state directory alone can't decrypt them. Each encrypted key is bound to its user, so copying it to another account's row doesn't decrypt either. Without `MESSWA_SECRET_KEY` messwa runs, but can't save or read mess keys. If you lose or change it, everyone has to save their key again.

**Passwords and sessions.** Passwords are hashed with argon2id and must be at least 10 characters. Signing in sets an `HttpOnly`, `SameSite=Strict` session cookie that lasts 30 days. Sessions live in the database as SHA-256 hashes. Every change the pages make sends a per-session CSRF token and must come from the dashboard's own origin. Five wrong passwords for one name from one address lock that pair out for a minute. Changing a password signs out your other devices.

**Managing accounts** is CLI-only:

```sh
./messwa user add -admin shourya    # asks for a password
./messwa user add asha              # a member
./messwa user list
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
- SETTINGS in the header picks your messaging app: WhatsApp (`wa.me`) or Beeper Desktop (`beeper://compose/whatsapp/phone/...`). The message button then opens that app. The choice is saved in the browser. Beeper links only work in Beeper Desktop, so phones and tablets always open WhatsApp.
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

The board shows sellers' names and phone numbers from the groups and nothing from DMs. The sales page below adds the names and numbers of people you sold to, for admins only.

### My sales

`/sales.html` (the MY SALES button on the board) tracks the meals you sell with `deliver-qr`. Members get MY MEALS instead: the same ledger with their own registrations and no sales, regulars, price check or earnings. On the board's day track, a coin and the price replace the meal icon for any meal you sold or armed today.

- **Sales ledger.** Days across, meals down, for the last four weeks (arrow keys page by week). A sold slot shows the price, the mess, and the buyer's pixel head. Each buyer always gets the same head. An armed slot drains like a listing until the meal ends. ATE means you used the registration yourself. WASTED means the meal ended unsold and unused. Dashed slots are registered meals that haven't ended. Hovering shows the buyer, their number, when they paid, how fast the QR went out, and the median ask for that meal.
- **Saved this month.** The green bar is what your sales earned back against this month's mess bill. The striped part after it is what the month's wasted meals would have fetched at the median ask. The bill comes from the portal's `/registrations/bill`, the same read as IIITH-mess-MCP's `get_bill`: availed meals plus the projected cost of meals you're registered for, so it grows as you register. When the portal can't be read, the page uses `sales.monthly_fee` from `config.toml` instead.
- **Regulars.** Every buyer, all time, with the number of meals they bought from you. Repeat buyers climb rarity tiers: 1 COMMON, 2-3 UNCOMMON, 4-6 RARE, 7-9 EPIC, 10+ LEGENDARY.
- **Price check.** Your sales over four weeks against the market's middle half and median ask for each meal, with UNDERCUT, AT MARKET or PREMIUM from your average gap to each serving's median.
- **Earnings.** Meals sold, money earned and median price per mess, with earnings per day on one shared scale.

Eaten, wasted and registered meals and the bill come from the portal, read with the signed-in person's own mess API key, so the page needs that key saved and the IIIT network, like `my_registrations`. The page reads registrations and the bill live and keeps each person's last read in memory for a minute. Without the portal it shows sales only and says why.

The median ask is the median stated price of sell listings for that serving at that mess. With fewer than 3 asks it falls back to every mess for that serving, then to that meal over four weeks. Listings are pruned after 14 days, so `run` saves each serving's asking prices every hour in `market_prices`.

Buyer names are the WhatsApp name on the payment message, or the one stored for that DM peer. Sales from before this page existed have no payment time, offer time or mess. The ledger fills in mess and diet from the portal when it can.

### Insights

`/insights.html` is for admins only. It's the INSIGHTS button on the board, which members don't see. It charts the market from `listing_facts`. Listings, posts and reactions are pruned after 14 days, so every hour `run` writes one permanent row per listing whose meal has ended. The first run after upgrading backfills whatever the last 14 days still hold. A row keeps updating for 7 days after its meal, so a late tick or un-tick still counts, and is final after that. Rows are keyed by the listing's first post, so `rebuild` updates them instead of adding copies.

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
| `list_recent_chats` | DM peers in the last 24h (max 72h): saved or not, new or not, last in/out. No content. Admins only |
| `read_unsaved_chat` | Message text for one unsaved number. Errors for saved contacts. Admins only |
| `my_registrations` | Live portal registrations for a date range, including cancellation and usage fields. Uses the caller's own mess API key |
| `my_qr` | Current portal QR as a PNG image. Sensitive; fetch again after token resets. Uses the caller's own mess API key |

All tools are read-only.

Each MCP request acts as a messwa user. A client sends `Authorization: Bearer mwa_...`, a token made on the account page or with `messwa user token`. Tokens are stored as SHA-256 hashes, shown once, and stop working on the next request after they're revoked or their user is disabled. Two more ways in act as the owner (the first admin):

- No `Authorization` header, allowed only when `mcp.listen` is loopback and `MESSWA_MCP_TOKEN` is unset. This is how the Docker setup works today.
- The shared `MESSWA_MCP_TOKEN`, kept so existing clients don't break. Setting it turns tokenless access off. Group posts and DM text are written by strangers. The server's instructions tell the agent to treat them as untrusted, but read-only tools are what actually limits the damage.

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

./messwa login                       # asks for your number (pairing code) or Enter for a QR
./messwa groups                      # copy the mess groups' JIDs into config.toml
./messwa user add -admin <you>       # your dashboard account
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

`/healthz` returns 503 when the WhatsApp connection is down.

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

The first buyer must reply privately to the quoted post within ten minutes.
Later buyers who quote the same offer join the queue in arrival order. Only
the current buyer gets payment instructions. That buyer has up to two minutes
to pay or can reply `not interested` to give up their turn. The bot finishes checking
any payment it received on time before it moves on to the next buyer. A
payment from a previous buyer cannot release the QR after their turn ends.
Once the QR is sent, the bot privately replies `sorry sold` to everyone still
in the queue. The queue also closes when the meal ends. The example sends the
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
The defaults post what `deliver-qr -group` always posted: one group, price
included, no extras.

| Setting | Flag | Default | Does |
| --- | --- | --- | --- |
| `groups` | `-group`, repeatable | none | Groups to post to, by exact name or JID, in order |
| `group_count` | `-group-count` | 0 | Post to the first N of the top-level `groups` instead. Set this or `groups`, not both |
| `post_gap` | `-post-gap` | `15s` | Pause between copies, at least 3s |
| `show_price` | `-show-price` | `true` | Put the price in the group post |
| `mention_extras` | `-mention-extras` | `false` | List the extras registered for that meal |

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
apart. Once the meal sells, no more copies go out. If one copy fails to post,
the others still go out and replies to the failed one still count, in case
WhatsApp posted it anyway. The command stops only when no copy posts.

**Extras.** The extras come from the portal's `/registrations/extras`, the
same read as IIITH-mess-MCP's `get_extra_registrations`. Extras already eaten
or registered at another mess are left out. If the read fails, the offer goes
out without extras and the log says why.

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

Normal text, screenshots, other buyers, expired messages, mismatched payment
details and known incomplete transactions do not qualify. The WhatsApp event
does not independently verify that funds arrived.

The data database stores the buyer, amount, image hash or portal-account binding, arming time, payment
message ID, outgoing image ID and state for each date/meal. It never stores
the raw payment payload. Repeated events and restarts cannot automatically
send the same slot twice. An armed slot can resume only with the same buyer,
price and image. A payment message cannot claim another meal.

Network failures or a crash after claiming leave the delivery as `sending`.
The command refuses to rearm `sending` or `sent` slots. Check the buyer's chat
and make any follow-up manually; a timeout does not prove that delivery failed.
The `sent` state records WhatsApp's send acknowledgement, not the buyer opening
the image. Keep the same state database when restarting. Do not run two
connections using the same linked-device session.

This command is not exposed through MCP and does not implement automatic
registration imports, scheduling, negotiation, buyer reassignment or refunds.

### Payment diagnostic

`inspect-payment` listens to one explicitly selected DM, including a saved
contact when selected. Stop `run` before reusing its session.

```sh
go build -o messwa .
# First setup only: copy config.example.toml to config.toml and run messwa login.
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

The plan from `/home/sdixit/messbuysell/selling-instructions.md`, adapted to WhatsApp. You start a sell task (meal, price range). The agent posts "Selling Kadamba lunch" in the groups without a price, answers unsaved numbers who DM about it, negotiates within the range, then hands the chat to you. The binary enforces the rules: sends happen only during an active task, only to unsaved numbers who messaged first, never below the floor price, and rate-limited.
