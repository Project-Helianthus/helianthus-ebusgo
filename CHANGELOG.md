# Changelog

## Unreleased

- Rename `protocol/telegram_fsm` to `protocol/telegramfsm` and
  `protocol/ebus_standard` to `protocol/ebusstandard`. The `types` package is
  retained. Consumers must update imports; the old paths have no forwarding
  packages. Protocol bytes and native data are unchanged.
