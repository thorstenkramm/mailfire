#!/usr/bin/env bash
#
# Send the templates.md demo message once per template, so all four
# templates can be checked in a real mail client.
#
# Usage: ./template-test.sh <to-address> [mailfire flags...]
#
# Extra flags are passed to every mailfire call, e.g. -c ./mailfire.conf
# or --test. mailfire is built from this checkout first, so the mails
# always show the current templates.

set -euo pipefail

if [[ $# -lt 1 || -z "$1" || "$1" == -* ]]; then
  echo "Usage: $0 <to-address> [mailfire flags...]" >&2
  exit 1
fi
to="$1"
shift

repo="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

echo "Building mailfire ..."
(cd "$repo" && go build -o "$work/mailfire" .)

cat >"$work/body.md" <<'EOF'
# Nightly backup finished

All five hosts were backed up last night. The run took **42 minutes**, two minutes faster than the weekly average.

| Host    |   Files |    Size | Status  |
|---------|--------:|--------:|---------|
| web01   |  18,204 |  3.1 GB | ok      |
| web02   |  18,197 |  3.1 GB | ok      |
| db01    |   1,322 | 41.7 GB | ok      |
| files01 | 412,880 | 96.4 GB | retried |
| ops01   |   9,415 |  1.2 GB | ok      |

> [!NOTE]
> The next full backup runs on Sunday at 02:00.

> [!TIP]
> List the snapshots of a single host with `restic snapshots --host files01`.

> [!IMPORTANT]
> Restore tests are due this month. Pick a host and restore at least one directory.

> [!WARNING]
> files01 timed out on the first attempt and was backed up on the second.

> [!CAUTION]
> Snapshots older than 90 days will be deleted on Monday.

[Open the backup dashboard](https://example.com/backups)
EOF

cat >"$work/footer.html" <<'EOF'
Sent automatically by the nightly backup job on ops01. <a href="https://example.com/backups/settings">Change who gets this report</a>.
EOF

for template in card card-serif plain plain-serif; do
  echo "Sending template $template to $to ..."
  "$work/mailfire" --md --template "$template" \
    --to "$to" \
    --subject "mailfire template test: $template" \
    --preheader "All five hosts were backed up; files01 needed a retry." \
    --footer "$work/footer.html" \
    --file "$work/body.md" \
    "$@"
done

echo "Sent 4 emails to $to."
