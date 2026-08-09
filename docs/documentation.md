---
layout: docindex
title: Documentation
permalink: /documentation/
eyebrow: documentation
summary: >-
  Three short paths through these pages, depending on whether you are adopting
  the library, evaluating it, or extending it. Every page has one job and links
  out rather than repeating another.
---
{%- assign u_why = '/documentation/why/' | relative_url -%}
{%- assign u_started = '/documentation/getting-started/' | relative_url -%}
{%- assign u_kit = '/documentation/kit/' | relative_url -%}
{%- assign u_model = '/documentation/model/' | relative_url -%}
{%- assign u_concepts = '/documentation/concepts/' | relative_url -%}
{%- assign u_reference = '/documentation/reference/' | relative_url -%}
{%- assign u_operations = '/documentation/operations/' | relative_url -%}
{%- assign u_architecture = '/documentation/architecture/' | relative_url -%}
{%- assign u_contributing = '/documentation/contributing/' | relative_url -%}
{%- assign u_design = '/documentation/design/' | relative_url -%}
{%- assign u_patches = '/documentation/patches/' | relative_url -%}

## Pick a path

Every page has one job. The *how* pages tell you what to call; the *why* pages
argue the decisions behind it. They link to each other and never repeat each
other, so you can read one without the other.

**Adopting it** — [why it exists]({{ u_why }}) →
[getting started]({{ u_started }}) → [the kit]({{ u_kit }}), with
[reference]({{ u_reference }}) open when you need the exact contract of a call.
Getting started is one program grown five times; it does not ask you to read
anything else first.

**Evaluating it** — [why it exists]({{ u_why }}) for the problem,
[design decisions]({{ u_design }}) for every choice and what it cost,
[concepts]({{ u_concepts }}) for the prior art it borrows from,
[operations]({{ u_operations }}) for what running it costs. If you know git,
[the git model]({{ u_model }}) is the fastest way to see the whole shape.

**Extending it** — read the three *Understand* pages in order:
[the git model]({{ u_model }}) for the vocabulary,
[architecture]({{ u_architecture }}) for the map onto Clean Architecture names,
then [design decisions]({{ u_design }}) for which of those choices are load-
bearing before you change one. [Contributing]({{ u_contributing }}) is the
invariants, the file-by-file reading order for the source, and how to write a
backend; [reference]({{ u_reference }}) is the contract your backend must meet.

**Optional side paths**, safe to skip until you need them:
[sealing RFC 6902 patches]({{ u_patches }}) if a client sends you JSON Patch,
and [operations]({{ u_operations }}) when you are ready to run it for real.

## Pick a backend

Adapters are optional sibling modules; the core never imports one. Implement the
3-method `Log` yourself and prove it with `conformance.RunLogConformance`.

<div class="tablewrap">
<table>
  <thead>
    <tr><th>Backend</th><th>Module</th><th>Consistency</th><th>Log conformance</th><th>Serializable append</th></tr>
  </thead>
  <tbody>
    {%- for b in site.data.backends %}
    <tr>
      <td><code>{{ b.ctor }}</code></td>
      <td><code>{{ b.module }}</code></td>
      <td>{{ b.consistency }}</td>
      <td class="ok">✓</td>
      {%- if b.serializable == "yes" %}
      <td class="ok">✓ {{ b.serializable_note }}</td>
      {%- else %}
      <td class="na">— {{ b.serializable_note }}</td>
      {%- endif %}
    </tr>
    {%- endfor %}
  </tbody>
</table>
</div>

Choose `adapters/sql` when concurrent writers must form one linear chain per
document; `adapters/clickhouse` for cheap columnar retention and analytics where
producers serialize per document. The full comparison, and the operational
detail behind it, is in
[operations]({{ '/documentation/operations/' | relative_url }}).
