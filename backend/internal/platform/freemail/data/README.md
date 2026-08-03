# Vendored consumer-mail-domain dataset

`providers.txt` is a byte-identical copy of `generate/domains.txt` from
[`goware/emailproviders`](https://github.com/goware/emailproviders), MIT licensed
(© 2015 Pressly Inc.). The grant is reproduced in `LICENSE-emailproviders.txt`
and must ship with any distribution of this software.

Vendored at commit state of 2025-02-08 (`remove xwaretech domains`), 8 758 lines.

## Why this source and not the popular ones

A domain list is only as clean as the chain it came from, and the two most
widely used npm packages have a chain we cannot use in a commercial product:

- **`Kikobeats/free-email-domains`** carries an MIT badge, but its `domains.json`
  is generated at install time from HubSpot's marketing CSV
  (`f.hubspotusercontent40.net/hubfs/…/free-domains-2.csv`). That is a
  competitor's curated asset with no license grant, and the MIT comes from
  someone who never owned the data. The EU database right (Directive 96/9/EC)
  protects exactly that kind of curated investment.
- **`willwhite/freemail`** is ISC, but `data/free.txt` is aggregated from about
  thirteen unlicensed GitHub gists. Same shape, milder.

`goware/emailproviders` commits the data file inside the MIT-licensed repository
itself and its generator is a plain txt→Go map, so the grant actually covers what
we copy.

## Known upstream defects

The file is vendored verbatim and sanitized at load time (`baseline.go`), so a
re-sync is a clean overwrite. These are the defects the sanitizer handles:

| Line | Content | Handling |
|---|---|---|
| 711 | `atlanticbb.net ` (trailing space) | trimmed |
| 3089 | `housefancom` (no dot) | dropped — cannot be a mail domain |
| 5829-5831 | `müll.email`, `müllemail.com`, `müllmail.com` | IDNA-folded to punycode, which is what a mail header carries |
| 8758 | `zzom.co.uk0-mail.com` | a missing newline glued `zzom.co.uk` and `0-mail.com`; the glued string is harmless (no mail domain equals it), but `0-mail.com` is therefore MISSING from the dataset and is carried in `pinnedBaseline` instead |

## Re-syncing

```
curl -sSL -o providers.txt https://raw.githubusercontent.com/goware/emailproviders/master/generate/domains.txt
curl -sSL -o LICENSE-emailproviders.txt https://raw.githubusercontent.com/goware/emailproviders/master/LICENSE
```

Then run the package tests: they assert the sanitizer still drops what it should
and that every domain in `pinnedBaseline` is still matched.
