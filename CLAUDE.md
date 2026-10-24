# RTFM

Roller derby Tournament Fixture Maker. `README.md` describes the features,
how sheets are read, running, Docker and k3s.

- `make test` (vet + tests, `-mod=vendor`); full tests need `../sanctioning`, `../infopacks`, `../statsbooks` and skip without. `make run` serves on :4200 with data in `data/`.
- `crgformat` is vendored from `../crg-format` (`replace` in go.mod): `make vendor` after changing it there. Statsbook and IGRF writing lives in crg-format (`statsbook/export.go`), infopack reading in `crg-format/crews/infopack.go`, game building in `crg-format/prepare`.
- Deployed at https://rtfm.quagga.rocks, but `deploy/k8s/ingress.yaml` keeps the placeholder host rtfm.example.org on purpose. Don't put the real host in the repo.
- Never read the sanctioning sheet's Contacts tab or charters' legal-name column.
- Commit on main; push only when asked.
