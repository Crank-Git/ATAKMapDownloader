# Vendored libraries

The binary embeds these libraries, so the web UI loads with no internet (FR-offline-web-ui-1, FR-offline-web-ui-5). Each file is a copy of the file in the npm registry tarball, with no change. The full license text of each library is in its `LICENSE` file.

| Library | Version | License | Source |
|---|---|---|---|
| Leaflet | 1.9.4 | BSD-2-Clause | https://registry.npmjs.org/leaflet/-/leaflet-1.9.4.tgz |
| Leaflet-Geoman (free edition, `@geoman-io/leaflet-geoman-free`) | 2.20.8 | MIT | https://registry.npmjs.org/@geoman-io/leaflet-geoman-free/-/leaflet-geoman-free-2.20.8.tgz |

The registry gives these `integrity` values for the tarballs. The tarballs that the files come from matched them:

- `leaflet-1.9.4.tgz`: `sha512-nxS1ynzJOmOlHp+iL3FyWqK89GtNL8U8rvlMOsQdTTssxZwCXh8N2NB3GDQOL+YR3XnWyZAxwQixURb+FA74PA==`
- `leaflet-geoman-free-2.20.8.tgz`: `sha512-LpfdCCwey3ptooujkke3PfI/eVBvJx1I7T0xx3qDnzi49/PDi+yK20gNEp/trfspuTodXtyL69PuecvlOKWtbw==`

## Files

`web/assets_test.go` compares each file with its SHA-256 below. If you replace a file, update its row.

| File | SHA-256 | Path in the tarball |
|---|---|---|
| `leaflet/leaflet.js` | `db49d009c841f5ca34a888c96511ae936fd9f5533e90d8b2c4d57596f4e5641a` | `package/dist/leaflet.js` |
| `leaflet/leaflet.css` | `a7837102824184820dfa198d1ebcd109ff6d0ff9a2672a074b9a1b4d147d04c6` | `package/dist/leaflet.css` |
| `leaflet/images/layers.png` | `1dbbe9d028e292f36fcba8f8b3a28d5e8932754fc2215b9ac69e4cdecf5107c6` | `package/dist/images/layers.png` |
| `leaflet/images/layers-2x.png` | `066daca850d8ffbef007af00b06eac0015728dee279c51f3cb6c716df7c42edf` | `package/dist/images/layers-2x.png` |
| `leaflet/images/marker-icon.png` | `574c3a5cca85f4114085b6841596d62f00d7c892c7b03f28cbfa301deb1dc437` | `package/dist/images/marker-icon.png` |
| `leaflet/images/marker-icon-2x.png` | `00179c4c1ee830d3a108412ae0d294f55776cfeb085c60129a39aa6fc4ae2528` | `package/dist/images/marker-icon-2x.png` |
| `leaflet/images/marker-shadow.png` | `264f5c640339f042dd729062cfc04c17f8ea0f29882b538e3848ed8f10edb4da` | `package/dist/images/marker-shadow.png` |
| `leaflet/LICENSE` | `53e8dc25862014e4324741ca18fbe3611e11d42ef69f59f86ea8c5389647d4cb` | `package/LICENSE` |
| `leaflet-geoman/leaflet-geoman.min.js` | `dbae663b1c6770b63767ab0fa8213ad82ebc53c97b8882b95bc6bf9c996aefbb` | `package/dist/leaflet-geoman.min.js` |
| `leaflet-geoman/leaflet-geoman.css` | `a0df8e0f6301f9f71d772b2c61913bb9094ae0735fd2223ccd1aecd871ea250c` | `package/dist/leaflet-geoman.css` |
| `leaflet-geoman/LICENSE` | `b8f81bc43788014a8be76982feb763d554b5b5218a1797fe4bedffd4df22e067` | `package/LICENSE` |

The `.js` files end with a `sourceMappingURL` comment. The source maps are not vendored, so the browser developer tools report a missing map on the server. The page itself sends no request for a map.
