#!/usr/bin/env bash

#
# Send a test message via the Gmail API.
#
# Needs a build with Mailfire's Google client secret ("make build" takes it
# from tmp/client_secret_*.json). The authorization is kept in the default
# location (~/.config/mailfire/gmail-credentials.json), so the browser
# sign-in only runs on first use or when the authorization is no longer
# valid. While the Google app is in testing, only registered test users can
# sign in, and authorizations expire after 7 days.
#
# Set the recipient with MAILFIRE_TEST_RECIPIENT; override the sender with
# GMAIL_TEST_FROM.
#

set -euo pipefail

: "${MAILFIRE_TEST_RECIPIENT:?export MAILFIRE_TEST_RECIPIENT first}"

FROM=${GMAIL_TEST_FROM:-"Gmail Test <p949566@googlemail.com>"}
TO=${MAILFIRE_TEST_RECIPIENT}

TEMP_DIR=$(mktemp -d)
trap 'rm -rf "${TEMP_DIR}"' EXIT
CONFIG_FILE=${TEMP_DIR}/mailfire-gmail.conf
LOG_FILE=${TEMP_DIR}/mailfire-gmail.log

cat << EOF > "$CONFIG_FILE"
[sending]
provider = "gmail"
from = "${FROM}"

[gmail]
timeout = "30s"

[logging]
logfile = "${LOG_FILE}"
loglevel = "debug"
EOF
chmod 0600 "$CONFIG_FILE"

# Sign in when there is no usable authorization (refresh proves it works).
if ! ./mailfire auth refresh --provider gmail -c "$CONFIG_FILE" > /dev/null 2>&1; then
  ./mailfire auth login --provider gmail -c "$CONFIG_FILE"
fi

./mailfire -c "$CONFIG_FILE" --to "$TO" -m "GMAIL TEST" --subject "GMAIL TEST"
cat "${LOG_FILE}"
