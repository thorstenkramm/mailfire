# Release notes

## Unreleased

- Large attachments are now composed into owner-only temporary spool files and streamed to Mailgun, Gmail, and SMTP,
  keeping memory use independent of attachment size. Deferred messages and resends stream the same stored payload.
- Gmail now uses the media-upload endpoint, raising Mailfire's complete-message limit from 25 MiB to the API's 35 MB
  limit. Gmail's 25 MB attachment limit is unchanged.
