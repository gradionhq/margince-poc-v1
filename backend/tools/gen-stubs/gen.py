#!/usr/bin/env python3
"""Regenerates internal/httpapi/stubs_gen.go from the ServerInterface in
crm-contracts/api_gen.go: one explicit 501 stub per contract operation.
Run from the repo root (make gen does)."""
import os
import re

ROOT = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
os.chdir(ROOT)

src = open('crm-contracts/api_gen.go').read()
block = re.search(r'type ServerInterface interface \{(.*?)\n\}', src, re.S).group(1)
methods = re.findall(r'^\t([A-Z]\w+)\((.*)\)$', block, re.M)

out = ['// Code generated from crm-contracts/api_gen.go ServerInterface; DO NOT EDIT.',
       '// Regenerate: make gen (tools/gen-stubs).',
       '',
       'package httpapi',
       '',
       'import (',
       '\tnethttp "net/http"',
       '',
       '\topenapi_types "github.com/oapi-codegen/runtime/types"',
       '',
       '\tcrmcontracts "github.com/gradionhq/margince/backend/crm-contracts"',
       '\t"github.com/gradionhq/margince/backend/internal/platform/httperr"',
       ')',
       '',
       '// stubs satisfies every crmcontracts.ServerInterface operation with an',
       '// explicit 501: the whole contract surface exists from day one, and an',
       '// unimplemented call is loud, never a silent 404. Server embeds stubs',
       '// (one level deep) and module handlers shadow the operations they implement.',
       'type stubs struct{}',
       '',
       'var _ crmcontracts.ServerInterface = stubs{}',
       '']
for name, params in methods:
    params = params.replace('http.ResponseWriter', 'nethttp.ResponseWriter').replace('*http.Request', '*nethttp.Request')
    parts = []
    for p in [q.strip() for q in params.split(',')]:
        toks = p.rsplit(' ', 1)
        if len(toks) == 2 and re.match(r'^[A-Z]', toks[1]):
            toks[1] = 'crmcontracts.' + toks[1]
        parts.append(' '.join(toks))
    out += [f'func (stubs) {name}({", ".join(parts)}) {{',
            f'\thttperr.NotImplemented(w, r, "{name}")',
            '}',
            '']
open('internal/httpapi/stubs_gen.go', 'w').write('\n'.join(out))
print(f'{len(methods)} stubs generated')
