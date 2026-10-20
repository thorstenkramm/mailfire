#!/usr/bin/env bash

#
# Send a test message via SMTP.
#
# Credentials are never stored in this script. Export them first, e.g. from
# a password manager:
#   export MAILFIRE_SMTP_USERNAME="postmaster@example.org"
#   export MAILFIRE_SMTP_PASSWORD="$(pass show mailgun/smtp)"
#
# Set the recipient with MAILFIRE_TEST_RECIPIENT.
#

set -euo pipefail

: "${MAILFIRE_SMTP_USERNAME:?export MAILFIRE_SMTP_USERNAME first}"
: "${MAILFIRE_SMTP_PASSWORD:?export MAILFIRE_SMTP_PASSWORD first}"
: "${MAILFIRE_TEST_RECIPIENT:?export MAILFIRE_TEST_RECIPIENT first}"

TEMP_DIR=$(mktemp -d)
CONFIG_FILE=${TEMP_DIR}/mailfire-smtp.conf
LOG_FILE=${TEMP_DIR}/mailfire-smtp.log

cat << EOF > "$CONFIG_FILE"
[sending]
provider = "smtp"
domain = "example.com"
from = "SMTP Test <user@httpe.org>"

[smtp]
host = "smtp.eu.mailgun.org"
port = 587
security = "starttls"
timeout = "30s"

[logging]
logfile = "${LOG_FILE}"
loglevel = "debug"
EOF
chmod 0600 "$CONFIG_FILE"

./mailfire -c "$CONFIG_FILE" --to "$MAILFIRE_TEST_RECIPIENT" -m "SMTP TEST" --subject "SMTP TEST"
cat "${LOG_FILE}"
rm -rf "${TEMP_DIR}"