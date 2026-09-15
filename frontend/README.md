# React frontend

Admin manages accounts, users and configuration; Portal provides the entrypoint; Usage shows personal usage. Native account pages require an administrator and allow only management URLs on the loopback allowlist.

## Languages

English is the default, regardless of the browser language. The language selector offers **English** and **简体中文** on the portal, Admin, Usage, sign-in, setup and native account pages. Ant Design controls, date pickers, numeric displays, validation and application notices follow the selected language. The business timezone remains independent.

The `cpa-ui-language` preference is stored in local storage and a host cookie so the three development ports share it. An explicit `?lang=en` or `?lang=zh-CN` takes priority and works when storage is unavailable. Switching reloads the current URL while preserving route parameters, fragment, theme and authentication; save any unsaved form changes first.

`src/i18n/en/` is the primary English catalog. The matching `src/i18n/zh-CN/` catalog uses the same stable IDs; wording changes never rename IDs. Shared messages load with the shell; Admin and Usage catalogs load with their feature modules to keep initial bundles within the existing size limits. Import the corresponding `src/i18n/admin` or `src/i18n/usage` registration before evaluating feature labels. Keep complete sentences and use `{0}`, `{1}`, etc. for interpolation.

Translate authored display fields only. Account IDs, user-entered names, API keys, configuration values, confirmation protocol tokens and diagnostic output retain their original bytes. The API client sends `Accept-Language` and displays localized backend messages directly. Reports follow that language; notifications use `system.language` (English by default). See [internationalization](../docs/internationalization.md) for the HTTP contract, design comparison and extension checklist.

## Development

See the [development guide](../docs/development.md#前端热更新) for dependencies, backend proxying and hot reload. Run from this directory:

```sh
npm ci
npm run typecheck
npm test
npm run build
npm run test:e2e
```

## Pages and data

- Ant Design provides base components; React Hook Form/Zod handle forms. Reuse existing navigation, tables, dialogs and feedback.
- Pages request only data for the current view and cover loading, empty, error, success and submission failure states.
- TanStack Query owns request lifecycles. Its cache is not a business fact or authorization source. The default cache is zero; Usage and other pages may explicitly cache by user and time range.
- `/overview/summary` returns counts only. User quotas are read separately; Gateway activation depends on Collector snapshots.
- `/site-config.json` exposes only public branding, email domains and client export fields. [OpenAPI](../api/openapi.yaml) defines the configuration APIs.

## Credentials and verification

Management keys, CSRF tokens and API keys never enter URLs, local storage or session storage. Full credentials from creation, rotation and resets remain in one-time mutations/dialogs and are cleared on close.

See [Configuration Center](../docs/configuration-center.md) for its interactions. The [English language matrix](e2e/language.spec.ts) covers defaults, switching, cross-entry persistence, English pages and preserved user content. The [Chinese regression matrix](e2e/visual.spec.ts) covers desktop, narrow, mobile, themes and error states. Inspect actual screenshots before updating visual baselines.
