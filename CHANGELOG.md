# Changelog

## [0.1.21](https://github.com/sholdee/drydock/compare/v0.1.20...v0.1.21) (2026-06-11)


### Bug Fixes

* **diff:** broaden identical document normalization fast path ([4d787b2](https://github.com/sholdee/drydock/commit/4d787b2feaf0897966f0b52c0558ffe92655080e))


### Performance Improvements

* **acquisition:** exclude .git from git snapshot copies ([ebbfaf0](https://github.com/sholdee/drydock/commit/ebbfaf02b0c6ce481df78885877da51468992518))
* **acquisition:** share one snapshot session across build phases ([ed3ee79](https://github.com/sholdee/drydock/commit/ed3ee796abf0896b8bcbb46e8eb4a02ad506b707))
* **app:** build diff sides concurrently ([c7abba7](https://github.com/sholdee/drydock/commit/c7abba7cac654339c895fed3659a3656fda6d0f3))
* **app:** clone render cache entries outside the cache mutex ([cb35acd](https://github.com/sholdee/drydock/commit/cb35acd7641902cfd544d8d0237e6226ff52dda5))
* **app:** memoize discovery scans and appset generation ([a4932bc](https://github.com/sholdee/drydock/commit/a4932bc6cba2314683f95e03bc1cc5f0e666549f))
* **app:** reuse list-phase caches in selector builds ([4c70fe9](https://github.com/sholdee/drydock/commit/4c70fe9f6b293ff4adf13fc7c0c0508bbd91e0bc))
* **app:** reuse list-phase discovery in the build phase ([cbebde1](https://github.com/sholdee/drydock/commit/cbebde1073523264097c3ad8dc6247957b142e9b))
* **app:** skip git ref snapshots when changed-only finds no paths ([0db576c](https://github.com/sholdee/drydock/commit/0db576cb3b0871215943c79aca8919e9b4c8d955))
* **change:** parallelize content comparisons in change detection ([1e9d05d](https://github.com/sholdee/drydock/commit/1e9d05d5a4038a69c0aec9b03929a980addc99a9))
* **change:** refine parallel change helpers ([3eb60b5](https://github.com/sholdee/drydock/commit/3eb60b5e8a3387c383ed10696e1868362e5b8dd5))
* **cli:** default render commands to parallel application rendering ([b6777fe](https://github.com/sholdee/drydock/commit/b6777fec6c02af7ad9639f0299dd0b5663e351a2))
* **diff:** skip normalization for byte-identical document pairs ([c2a72ad](https://github.com/sholdee/drydock/commit/c2a72addac5f884930fed00ca19ca36ec4a52cc8))
* remediate render hot paths ([d952785](https://github.com/sholdee/drydock/commit/d9527853e08a1641a7bf924abe431195a6281eb2))
* **render:** cache loaded helm charts within a render session ([1ab0cb3](https://github.com/sholdee/drydock/commit/1ab0cb3046e9fb819a750d4949509e28ba56a34e))


### Miscellaneous Chores

* **deps:** update golang:1.26.4 docker digest to 87a41d2 ([#136](https://github.com/sholdee/drydock/issues/136)) ([d407dd3](https://github.com/sholdee/drydock/commit/d407dd38c4eebdc37e0484e53e54310ac7f80576))
* **deps:** update golang:1.26.4 docker digest to d184d9b ([#135](https://github.com/sholdee/drydock/issues/135)) ([e1c6582](https://github.com/sholdee/drydock/commit/e1c65821ae1f96ef4794f39e82c607558c9fc87e))
* **deps:** update golang:1.26.4 docker digest to d47ca13 ([#132](https://github.com/sholdee/drydock/issues/132)) ([f7cd362](https://github.com/sholdee/drydock/commit/f7cd36239cd31086f84492c6acc6abda023567d6))


### Code Refactoring

* **app:** split diff orchestration helpers ([7e68d4e](https://github.com/sholdee/drydock/commit/7e68d4eb0d27430b2f8e4b5328ff4ca064ec4971))

## [0.1.20](https://github.com/sholdee/drydock/compare/v0.1.19...v0.1.20) (2026-06-10)


### Features

* add plugin policy onboarding ([73aa4ec](https://github.com/sholdee/drydock/commit/73aa4ecbd4d55ce9c47f27d34f5e6d7f7fcd76fe))
* add plugin policy onboarding ([01e2348](https://github.com/sholdee/drydock/commit/01e2348e53aae7d53beec82cf3ff8b1da2deef14))
* default avp compatibility for plugin sources ([024cbaf](https://github.com/sholdee/drydock/commit/024cbaf10aeae7cecf96970f44e1112f6890e145))
* infer native plugin policy engines ([54cd073](https://github.com/sholdee/drydock/commit/54cd0734a262f1462e85c5418af7aa14eb3807ac))


### Bug Fixes

* **deps:** update module golang.org/x/crypto to v0.53.0 ([#121](https://github.com/sholdee/drydock/issues/121)) ([5c08df0](https://github.com/sholdee/drydock/commit/5c08df0a42bfe6bb348d60321e1e30db701c208d))


### Documentation

* clarify plugin policy onboarding ([842059c](https://github.com/sholdee/drydock/commit/842059ce56a64457d20919b089b88bf9a48d4eb2))
* consolidate applicationset reference ([bb5ac89](https://github.com/sholdee/drydock/commit/bb5ac89612293b94b0667724292b82080edebce2))
* consolidate github actions reference ([3afb243](https://github.com/sholdee/drydock/commit/3afb243f5cf2c461151bd7638bf13ee96d594393))
* consolidate reference documentation ([c64f644](https://github.com/sholdee/drydock/commit/c64f64430ca07eef3f1f8e84cb7ea482e1503f20))
* consolidate source acquisition reference ([6994709](https://github.com/sholdee/drydock/commit/6994709a35f2f6be80bb89b4460b3c7acc7e34de))
* consolidate topology reference ([13a48cc](https://github.com/sholdee/drydock/commit/13a48cca98c7c973f8fedb0881b1895f5431ae7d))
* consolidate usage reference ([c0c471b](https://github.com/sholdee/drydock/commit/c0c471b03b35295c05b57d5cd4c0e29dbd0ee875))
* improve docs code block highlighting ([#125](https://github.com/sholdee/drydock/issues/125)) ([017d3bb](https://github.com/sholdee/drydock/commit/017d3bb51799f44d319d9a6c298e9f8299e3275b))
* link maintained go api reference ([f9a6928](https://github.com/sholdee/drydock/commit/f9a6928fe9b7dc5b68eddfd43fb2daec69a336ef))
* normalize maintainer doc headings ([84f2eff](https://github.com/sholdee/drydock/commit/84f2eff70ed663a50bf52d5afe0f920e8af8bbb3))
* port docs header github card ([#124](https://github.com/sholdee/drydock/issues/124)) ([93615f1](https://github.com/sholdee/drydock/commit/93615f12896cb03fba2b70ed73bdae872b881b56))
* publish curated site reference ([560dade](https://github.com/sholdee/drydock/commit/560dade79641a4ad06f515f508a4069db01ac57e))
* show list page next links ([caee47a](https://github.com/sholdee/drydock/commit/caee47a4f13d2eaffc30d91ff40427a41ed6c841))
* simplify reference navigation ([cb102d7](https://github.com/sholdee/drydock/commit/cb102d73845d947e1c653f6229f780bc31622757))
* use placeholder pr action version ([10680ea](https://github.com/sholdee/drydock/commit/10680eaacf1a35d478b05b6a610e8e6e1a205bbb))


### Miscellaneous Chores

* **deps:** update golang:1.26.4 docker digest to 11fd8f7 ([#130](https://github.com/sholdee/drydock/issues/130)) ([5ae2b9c](https://github.com/sholdee/drydock/commit/5ae2b9cdd0df06709e8f911ac61d01fe77742851))
* migrate yaml parser to v3 ([#128](https://github.com/sholdee/drydock/issues/128)) ([d1bfa82](https://github.com/sholdee/drydock/commit/d1bfa82aad66814ff7b558bd40556270ab00834a))

## [0.1.19](https://github.com/sholdee/drydock/compare/v0.1.18...v0.1.19) (2026-06-08)


### Bug Fixes

* **deps:** update module golang.org/x/sys to v0.46.0 ([#115](https://github.com/sholdee/drydock/issues/115)) ([fd6c2fc](https://github.com/sholdee/drydock/commit/fd6c2fc2dbe3f3bd64c7da62bd11e75c35f82a51))
* **deps:** update module golang.org/x/term to v0.44.0 ([#119](https://github.com/sholdee/drydock/issues/119)) ([5b6d231](https://github.com/sholdee/drydock/commit/5b6d231d26ac95296a0004397d993e6fcd4c24a6))
* support native AVP plugin compatibility ([#120](https://github.com/sholdee/drydock/issues/120)) ([5a35fb8](https://github.com/sholdee/drydock/commit/5a35fb8b3e25445436d9aaa249efa0c1ce9daaed))


### Miscellaneous Chores

* **deps:** update dependency hugo to v0.163.0 ([#117](https://github.com/sholdee/drydock/issues/117)) ([1f677f6](https://github.com/sholdee/drydock/commit/1f677f67917b3b15ec829bf2381d71681f6bde39))
* **deps:** update module golang.org/x/sync to v0.21.0 ([#114](https://github.com/sholdee/drydock/issues/114)) ([f3956e1](https://github.com/sholdee/drydock/commit/f3956e138f2a8283cbe832f0280034569b4ef4de))
* **deps:** update module golang.org/x/text to v0.38.0 ([#118](https://github.com/sholdee/drydock/issues/118)) ([50d1dde](https://github.com/sholdee/drydock/commit/50d1ddec2553810891cf5f42239cf6bc3202eb8c))

## [0.1.18](https://github.com/sholdee/drydock/compare/v0.1.17...v0.1.18) (2026-06-07)


### Features

* validate appproject policy semantics ([8dd2468](https://github.com/sholdee/drydock/commit/8dd2468dd1876adb347f819d30a810aecec55e1c))
* validate AppProject policy semantics ([bd2ed62](https://github.com/sholdee/drydock/commit/bd2ed62498203c239df5505809b15515fcd37009))


### Documentation

* align image comment header ([#112](https://github.com/sholdee/drydock/issues/112)) ([a80fe18](https://github.com/sholdee/drydock/commit/a80fe18f7e722039e054bb4ead6ff490a14ae83b))
* document appproject compatibility semantics ([e7a1b18](https://github.com/sholdee/drydock/commit/e7a1b186e425c398ed1a38c4428b2669053f8ec9))
* showcase rendered diff view ([#110](https://github.com/sholdee/drydock/issues/110)) ([fb97574](https://github.com/sholdee/drydock/commit/fb97574bec230abc35f5723697a4dcd50c6bb313))

## [0.1.17](https://github.com/sholdee/drydock/compare/v0.1.16...v0.1.17) (2026-06-06)


### Bug Fixes

* size diff view to mobile viewport ([b344c76](https://github.com/sholdee/drydock/commit/b344c760c55d1e26151a369fd821e31f9a6e5379))


### Documentation

* add drydock domain context ([50dfc72](https://github.com/sholdee/drydock/commit/50dfc7253baa9d3852a10b1ad9a88f40a7f0439a))


### Code Refactoring

* clarify desired-state render orchestration ([a51bc07](https://github.com/sholdee/drydock/commit/a51bc07c57d0cdfe6ea92b3a171bc226576923a0))
* isolate build and diff side orchestration ([cba90d7](https://github.com/sholdee/drydock/commit/cba90d79475284ff6d034db6e32a986dcda3ae65))
* isolate policy plugin render planning ([12058e5](https://github.com/sholdee/drydock/commit/12058e5e538e4a5a18dc996dcd0e92958c1b9d33))
* split html diff report internals ([de02a93](https://github.com/sholdee/drydock/commit/de02a938890494ea7f840b52a873ee8d4c478c5a))

## [0.1.16](https://github.com/sholdee/drydock/compare/v0.1.15...v0.1.16) (2026-06-05)


### Features

* add diff sidebar keyboard navigation ([9026622](https://github.com/sholdee/drydock/commit/9026622fb3d74ced17df57226dea0df01201ee81))
* clarify html diff resource headings ([2bdfc3b](https://github.com/sholdee/drydock/commit/2bdfc3ba768068b6122535ae77ce7da2fc740dd1))
* refine HTML diff report UX ([41cae90](https://github.com/sholdee/drydock/commit/41cae90fbff19cb9d27c60fabe2ea264354a60d3))


### Bug Fixes

* shorten diff comment heading ([1ab2eeb](https://github.com/sholdee/drydock/commit/1ab2eeb9b74854b0ccbfe20bc2cc52275643abb2))


### Documentation

* refresh pr comment example ([a06aa67](https://github.com/sholdee/drydock/commit/a06aa675fe30de2a8a0b779d620de296ff3104ef))

## [0.1.15](https://github.com/sholdee/drydock/compare/v0.1.14...v0.1.15) (2026-06-05)


### Features

* harden html diff default selection ([f2aa636](https://github.com/sholdee/drydock/commit/f2aa636f8a440436404a34e292de1469dff71992))
* highlight yaml in html diff rows ([87ac277](https://github.com/sholdee/drydock/commit/87ac277ea5f527395116c3d2b0f494a19d7f752b))
* highlight yaml in lazy html diffs ([8fd925c](https://github.com/sholdee/drydock/commit/8fd925ce9447ba8260ee19d34ee97e0e78f6c2f2))
* lazily render large html diffs ([f21d32f](https://github.com/sholdee/drydock/commit/f21d32f53561c7d55f6df0710f13cc4cf3085eaf))
* refine html diff view ([b9f6327](https://github.com/sholdee/drydock/commit/b9f632758eb19caa141757125a156816ae15edf6))
* refine rendered html diff view ([8ff1636](https://github.com/sholdee/drydock/commit/8ff1636051995d15c6ace0ca08d1c16947b32b7c))


### Documentation

* describe plugin policy schema fields ([#102](https://github.com/sholdee/drydock/issues/102)) ([012c549](https://github.com/sholdee/drydock/commit/012c549216c278ebc22154a2de33a717ee35c71d))
* refresh rendered diff example ([4399f87](https://github.com/sholdee/drydock/commit/4399f87ba9de244ee0b6f65f8734ae13b6055301))


### Styles

* mask sticky diff header background ([ae2a8ba](https://github.com/sholdee/drydock/commit/ae2a8ba78cf0fee744b63fe57328b0c79ed758a4))
* tune rendered diff spacing ([604a2a2](https://github.com/sholdee/drydock/commit/604a2a29c5aec6fad4e42905cb02d7595bae9671))

## [0.1.14](https://github.com/sholdee/drydock/compare/v0.1.13...v0.1.14) (2026-06-04)


### Features

* **plugin-policy:** add container cache mounts ([#100](https://github.com/sholdee/drydock/issues/100)) ([2f640a5](https://github.com/sholdee/drydock/commit/2f640a504bebc3d72646e2ddd40d4914702a47cd))
* **plugin-policy:** add plugin cache dir option ([1e1c95b](https://github.com/sholdee/drydock/commit/1e1c95b6aa0b0b54da0a88f4881ddd1ef0121bce))
* **plugin-policy:** support trusted command-backed rendering ([9e1a37c](https://github.com/sholdee/drydock/commit/9e1a37cacdf407872946dceb405d3ab740e54445))
* **plugin-policy:** support trusted container bootstrap rendering ([7b4e7d9](https://github.com/sholdee/drydock/commit/7b4e7d99832a8694a64c12185ea68f8a55f736e9))
* **pr-action:** cache plugin mounts ([dc9ebcc](https://github.com/sholdee/drydock/commit/dc9ebccf85d283ab024622df6065fdfbda997ce5))
* **pr-action:** cache plugin mounts ([e25139e](https://github.com/sholdee/drydock/commit/e25139e6c221988d82ff69df35017735f96dc995))


### Bug Fixes

* accept core resource customization split keys ([#96](https://github.com/sholdee/drydock/issues/96)) ([13f07c0](https://github.com/sholdee/drydock/commit/13f07c0c66c03f1941ef744ca1358bc97b6b968b))
* **chart:** accept v-prefixed Helm chart versions ([ed2daca](https://github.com/sholdee/drydock/commit/ed2daca3d9f5bc56e2f3be751ed6266b1a5a5b0d))


### Documentation

* **plugin-policy:** document trusted container workflows ([08474d0](https://github.com/sholdee/drydock/commit/08474d041eb96371b1632f1fb0d75a879920318f))

## [0.1.13](https://github.com/sholdee/drydock/compare/v0.1.12...v0.1.13) (2026-06-04)


### Features

* refine full rendered diff view ([6218029](https://github.com/sholdee/drydock/commit/6218029b87e1c4942c7fa0539580f512370c4a83))


### Bug Fixes

* align rendered diff artifact action metadata ([bf77135](https://github.com/sholdee/drydock/commit/bf77135ca9c34d6c991562438d57bbb26e7e906b))


### Documentation

* document full rendered diff view workflow ([41d96c0](https://github.com/sholdee/drydock/commit/41d96c03eb17d00af46b0c7e74e9dca6b5be54f7))
* document full rendered diff workflow ([08a4b46](https://github.com/sholdee/drydock/commit/08a4b46df19bf4357f3d53a4340d9057bdc7697d))

## [0.1.12](https://github.com/sholdee/drydock/compare/v0.1.11...v0.1.12) (2026-06-03)


### Features

* add HTML diff artifact review UI ([#93](https://github.com/sholdee/drydock/issues/93)) ([b0ece3c](https://github.com/sholdee/drydock/commit/b0ece3cc18524b7fa40052e59dccd13443477a0b))


### Miscellaneous Chores

* **deps:** update actions/checkout action to v6.0.3 ([#85](https://github.com/sholdee/drydock/issues/85)) ([8842a65](https://github.com/sholdee/drydock/commit/8842a65abc1d6559b0694b8d058ceda008c60644))
* **deps:** update dependency go to v1.26.4 ([#88](https://github.com/sholdee/drydock/issues/88)) ([0043ff6](https://github.com/sholdee/drydock/commit/0043ff628c6aac997a601718cfdbdbd7561d41cc))
* **deps:** update dependency shinagawa-web/gomarklint to v3.2.3 ([#91](https://github.com/sholdee/drydock/issues/91)) ([332155c](https://github.com/sholdee/drydock/commit/332155c48422f8c0f19099cee8f0a175f42f6830))
* **deps:** update go module directive to v1.26.4 ([#89](https://github.com/sholdee/drydock/issues/89)) ([e23e66a](https://github.com/sholdee/drydock/commit/e23e66aa2ee286ea107e95c37e5c56225ce074fc))
* **deps:** update golang docker tag to v1.26.4 ([#90](https://github.com/sholdee/drydock/issues/90)) ([33094a0](https://github.com/sholdee/drydock/commit/33094a0e7542d6d70e1aeefffe43091f9fb2da89))
* **deps:** update golang:1.26.4 docker digest to 68cb6d6 ([#92](https://github.com/sholdee/drydock/issues/92)) ([ea3c46c](https://github.com/sholdee/drydock/commit/ea3c46c38c982e77aad3436f352440cc53ece6ec))

## [0.1.11](https://github.com/sholdee/drydock/compare/v0.1.10...v0.1.11) (2026-06-02)


### Bug Fixes

* improve diag success and trash handling ([#84](https://github.com/sholdee/drydock/issues/84)) ([e6697ab](https://github.com/sholdee/drydock/commit/e6697aba7cf2b7485c86fbd0699e3b9a7d19ab05))
* polish operator-facing CLI and release UX ([#83](https://github.com/sholdee/drydock/issues/83)) ([bacc1ab](https://github.com/sholdee/drydock/commit/bacc1ab9913bdb4926f8878fb52ac83e06852180))


### Documentation

* clarify render parity and README routing ([#81](https://github.com/sholdee/drydock/issues/81)) ([b952da5](https://github.com/sholdee/drydock/commit/b952da5d024284f385034fd45417fd55a4cfcbd3))

## [0.1.10](https://github.com/sholdee/drydock/compare/v0.1.9...v0.1.10) (2026-06-01)


### Features

* add changed-only path filters ([#78](https://github.com/sholdee/drydock/issues/78)) ([f2f13a2](https://github.com/sholdee/drydock/commit/f2f13a210529514c46b7224714923a9884d2b732))
* add GitHub Pages documentation site ([#66](https://github.com/sholdee/drydock/issues/66)) ([9914930](https://github.com/sholdee/drydock/commit/991493085d7b812df119549e5d924927ba223d75))
* improve install distribution UX ([#79](https://github.com/sholdee/drydock/issues/79)) ([82759d1](https://github.com/sholdee/drydock/commit/82759d1fa5adcef179ac1316501bc498183e3e3e))


### Bug Fixes

* compact docs header navigation ([#75](https://github.com/sholdee/drydock/issues/75)) ([57cd7fc](https://github.com/sholdee/drydock/commit/57cd7fcf315fb54750bba1119a4eaa0b29463ac9))


### Documentation

* add community files ([#77](https://github.com/sholdee/drydock/issues/77)) ([36d8127](https://github.com/sholdee/drydock/commit/36d81278630410d064fbca92ce92760d1ec7c060))
* explain render parity strategy ([#76](https://github.com/sholdee/drydock/issues/76)) ([be47ebe](https://github.com/sholdee/drydock/commit/be47ebea9208caa66d53ecdab5ca6789f5065fce))
* improve compatibility page readability ([#73](https://github.com/sholdee/drydock/issues/73)) ([4e2e45c](https://github.com/sholdee/drydock/commit/4e2e45cad1c42b1c0075219546862bd4e6f8ea53))
* polish operator documentation site ([#72](https://github.com/sholdee/drydock/issues/72)) ([66ffd6e](https://github.com/sholdee/drydock/commit/66ffd6e862616c963ee39a3254919cf95d906bc6))
* render Mermaid diagrams on docs site ([#71](https://github.com/sholdee/drydock/issues/71)) ([d53b79e](https://github.com/sholdee/drydock/commit/d53b79ef4bad4853a8e10737f94c2378a750f0a1))
* route README links to docs site ([#70](https://github.com/sholdee/drydock/issues/70)) ([736279b](https://github.com/sholdee/drydock/commit/736279b2fbddf35902384c7709cfa296dcf82542))

## [0.1.9](https://github.com/sholdee/drydock/compare/v0.1.8...v0.1.9) (2026-05-30)


### Features

* add markdown diff comments ([#63](https://github.com/sholdee/drydock/issues/63)) ([ecfa161](https://github.com/sholdee/drydock/commit/ecfa161cc04f2df2b1b6fe336f614770ec7c5c3a))
* add markdown image diff output ([#65](https://github.com/sholdee/drydock/issues/65)) ([6838fde](https://github.com/sholdee/drydock/commit/6838fde3ec1082cc9d7146d178c0003d622d50b4))

## [0.1.8](https://github.com/sholdee/drydock/compare/v0.1.7...v0.1.8) (2026-05-30)


### Features

* add CLI profiling diagnostics ([05ef4a8](https://github.com/sholdee/drydock/commit/05ef4a85a234e5ad6826dbf16de4c89a79110d86))
* add CLI profiling support ([8892efe](https://github.com/sholdee/drydock/commit/8892efe6fc9da295e374ea9b288a19967403edbf))
* add drydock pull request action ([#50](https://github.com/sholdee/drydock/issues/50)) ([a38b632](https://github.com/sholdee/drydock/commit/a38b6320a031392c8e4656010c396fb384cf916e))
* allow pr action to use existing drydock binary ([#55](https://github.com/sholdee/drydock/issues/55)) ([644ecb0](https://github.com/sholdee/drydock/commit/644ecb02a807260739f7a658f6b40a7e00569b92))
* cache drydock action installs ([#57](https://github.com/sholdee/drydock/issues/57)) ([92dae70](https://github.com/sholdee/drydock/commit/92dae7051f53c12517982c7647e513a582ad3998))


### Bug Fixes

* align mise config with renovate lock updates ([#52](https://github.com/sholdee/drydock/issues/52)) ([15eb089](https://github.com/sholdee/drydock/commit/15eb089a6fbb59a989d5ca9ee19f3e20bee88a65))
* avoid duplicate git auth headers in pr action ([4f9701f](https://github.com/sholdee/drydock/commit/4f9701fc89fdcc809626420cbb69aab0de0e813a))
* avoid duplicate git auth headers in PR action ([#56](https://github.com/sholdee/drydock/issues/56)) ([f8612e7](https://github.com/sholdee/drydock/commit/f8612e78899a1db21618f17200457c21424d4ad4))
* link pr action artifact comments ([#60](https://github.com/sholdee/drydock/issues/60)) ([bfee8e8](https://github.com/sholdee/drydock/commit/bfee8e8364c21b9025cf49cacd523e220a8aaf2a))
* summarize ConfigMap binaryData diffs ([cb1d9c4](https://github.com/sholdee/drydock/commit/cb1d9c4fb551611e6a3132c72394fbae5bb6b045))
* summarize ConfigMap binaryData diffs ([16ad2ff](https://github.com/sholdee/drydock/commit/16ad2ff0f68b641489b62e2f7a11aa732390b3d4))
* support older image diff output in pr action ([c224269](https://github.com/sholdee/drydock/commit/c22426938622f0d345fa2897756c89955eb79e32))


### Documentation

* document profiling workflow ([940cf61](https://github.com/sholdee/drydock/commit/940cf612495266a20e2028ef93632a0d4d8d031c))


### Miscellaneous Chores

* **deps:** update dependency lefthook to v2.1.9 ([#49](https://github.com/sholdee/drydock/issues/49)) ([1b612c0](https://github.com/sholdee/drydock/commit/1b612c0571b9bb43b574d2892db7974d3dbed4b8))
* **deps:** update mshick/add-pr-comment digest to 8e49278 ([#51](https://github.com/sholdee/drydock/issues/51)) ([dce3e72](https://github.com/sholdee/drydock/commit/dce3e72f1864a7958feb53485b36799b454d3d95))
* modernize Go lint rules ([#62](https://github.com/sholdee/drydock/issues/62)) ([e50094f](https://github.com/sholdee/drydock/commit/e50094fcbe46f8d8ef69f1662c0f3557124c0d2e))


### Code Refactoring

* improve code locality and CI guardrails ([8399c50](https://github.com/sholdee/drydock/commit/8399c50ac081cefd9355aa8060a8a35586ecc7b4))
* split app settings and diff helpers ([305351a](https://github.com/sholdee/drydock/commit/305351a7b9b884ec8b9a1bcb453fb4f34a6b960f))
* split helm renderer helpers ([740675c](https://github.com/sholdee/drydock/commit/740675c9b9fe301b3684fb3e9190990427a9dea1))
* split plugin policy parser helpers ([abfe213](https://github.com/sholdee/drydock/commit/abfe213f11cb6c3750982b5c90007f5f3d71b615))

## [0.1.7](https://github.com/sholdee/drydock/compare/v0.1.6...v0.1.7) (2026-05-29)


### Features

* report jsonnet module version ([2100c70](https://github.com/sholdee/drydock/commit/2100c7072e77f849ef7fd8e38dc90130889d93c1))


### Bug Fixes

* acquire helm chart dependencies ([4fed1e6](https://github.com/sholdee/drydock/commit/4fed1e6f1b7ca1669723a6a7da3e7ae87a649d1a))
* align applicationset semantics ([88e3a1d](https://github.com/sholdee/drydock/commit/88e3a1d5b9cf99d64da8daa1be533e5fc1ea1c53))
* align helm source semantics ([d735d1a](https://github.com/sholdee/drydock/commit/d735d1a3f3395ef431c44e22723a8e55d8061cbd))
* align offline Argo CD rendering semantics ([005a14e](https://github.com/sholdee/drydock/commit/005a14efdc6df64e80fff0ae535520cc07d4a6aa))
* align source selection semantics ([1414f6b](https://github.com/sholdee/drydock/commit/1414f6b53f18c6b176d69b7bd968e0a0bedf77b5))
* apply source kustomize options ([d09d7c7](https://github.com/sholdee/drydock/commit/d09d7c7a50830b2455a26d7aa31dd2d2e9aedaf0))
* apply tracking metadata and cache safety ([d1ac707](https://github.com/sholdee/drydock/commit/d1ac707bfda1b8ebdd4fdb6f10501a246ef62a1a))
* clarify runtime boundary diagnostics ([a2e48e3](https://github.com/sholdee/drydock/commit/a2e48e3583f854405e7b947537c586c44c84832f))
* render directory jsonnet safely ([788e1d3](https://github.com/sholdee/drydock/commit/788e1d38e93c06a73e57b746b21a2749c68df6c6))


### Documentation

* activate tracking and cache safety cases ([173b775](https://github.com/sholdee/drydock/commit/173b775594ca914b48c354e825053a6ef6cb1e1b))
* document helm source boundaries ([92b9e66](https://github.com/sholdee/drydock/commit/92b9e66bcef664d5142929c0d8ec806085c5fabb))

## [0.1.6](https://github.com/sholdee/drydock/compare/v0.1.5...v0.1.6) (2026-05-28)


### Features

* add native avp compatibility ([84346ad](https://github.com/sholdee/drydock/commit/84346ada9a8bcf5330937e4f8f1d5c6042fbdc39))
* add trusted exec plugin policy ([86546c4](https://github.com/sholdee/drydock/commit/86546c48fc429069a916651474efa1f07c945f31))
* add trusted plugin policy ([aa5e81a](https://github.com/sholdee/drydock/commit/aa5e81a0a51cc3ad72a36c8934c6508eb7884d40))
* add trusted plugin policy engines ([87966c4](https://github.com/sholdee/drydock/commit/87966c4cded5a2f797ad7edfa8e9c9c6eac9874f))
* add trusted plugin policy engines ([42e0c43](https://github.com/sholdee/drydock/commit/42e0c432df623d35ccd633362bf26b3a5e023a53))
* chain trusted exec post-renderers ([632371b](https://github.com/sholdee/drydock/commit/632371b0440b6996d2fbff9d97ca1b6ba1fdbd1e))
* report runtime module versions ([#44](https://github.com/sholdee/drydock/issues/44)) ([bd4f5ee](https://github.com/sholdee/drydock/commit/bd4f5ee7f2e1124d44eb841193d001b76a058024))
* report trusted exec plugin metadata ([c69ae4a](https://github.com/sholdee/drydock/commit/c69ae4a7394fc37a77b5fc170c7bb597b0473b06))


### Bug Fixes

* **deps:** update module github.com/argoproj/argo-cd/v3 to v3.4.3 ([#41](https://github.com/sholdee/drydock/issues/41)) ([3636af5](https://github.com/sholdee/drydock/commit/3636af5b3b606694a657de4a345b151d7e388740))


### Documentation

* gate selective native plugin engines ([5a2aade](https://github.com/sholdee/drydock/commit/5a2aade2a48278be0e4153f4928038ecf0769404))
* harden plugin policy UX ([7586bec](https://github.com/sholdee/drydock/commit/7586becfcb2cee5f771097a32df57b660f46f6ad))

## [0.1.5](https://github.com/sholdee/drydock/compare/v0.1.4...v0.1.5) (2026-05-27)


### Features

* color diff output ([#37](https://github.com/sholdee/drydock/issues/37)) ([77a8cf3](https://github.com/sholdee/drydock/commit/77a8cf31bf15d35c836e53f16cf77707ee905be5))


### Code Refactoring

* clarify discovery, rendering, and CLI plumbing ([#39](https://github.com/sholdee/drydock/issues/39)) ([ff12879](https://github.com/sholdee/drydock/commit/ff12879cfef15b833ab21ac4c744d61f0a5be77e))

## [0.1.4](https://github.com/sholdee/drydock/compare/v0.1.3...v0.1.4) (2026-05-27)


### Features

* discover rendered Argo CD fleets ([#35](https://github.com/sholdee/drydock/issues/35)) ([645684b](https://github.com/sholdee/drydock/commit/645684b05f66406be800f2b8c9ee4ada4018b14f))
* discover rendered kustomize apps ([#32](https://github.com/sholdee/drydock/issues/32)) ([a5db3bf](https://github.com/sholdee/drydock/commit/a5db3bf1486704f9450a444a3f6b47bb0fb1489b))

## [0.1.3](https://github.com/sholdee/drydock/compare/v0.1.2...v0.1.3) (2026-05-27)


### Features

* detect exact image fields ([#26](https://github.com/sholdee/drydock/issues/26)) ([36f24fd](https://github.com/sholdee/drydock/commit/36f24fdca939c149eb3d70b5f933c0127166d4e2))
* render native kustomize cmp sources ([#29](https://github.com/sholdee/drydock/issues/29)) ([a4eee98](https://github.com/sholdee/drydock/commit/a4eee980499829517ee52bf9891e546d154fa9f6))


### Documentation

* refactor documentation routing ([#27](https://github.com/sholdee/drydock/issues/27)) ([440f332](https://github.com/sholdee/drydock/commit/440f3328d8495d8d44bf92fb896b119e4f447a02))
* sharpen readme positioning ([#31](https://github.com/sholdee/drydock/issues/31)) ([a986b71](https://github.com/sholdee/drydock/commit/a986b71619f38639503a45ba244c513b86c6dd9e))


### Miscellaneous Chores

* **deps:** update dependency shinagawa-web/gomarklint to v3.2.2 ([#28](https://github.com/sholdee/drydock/issues/28)) ([1e34141](https://github.com/sholdee/drydock/commit/1e34141f8f13fd502a58a0836df3bc364cf5ed67))

## [0.1.2](https://github.com/sholdee/drydock/compare/v0.1.1...v0.1.2) (2026-05-27)


### Features

* publish immutable release artifacts ([31207c0](https://github.com/sholdee/drydock/commit/31207c039ac9582b03f18f9e7192df7ce77873f5))
* publish immutable release artifacts ([d28e166](https://github.com/sholdee/drydock/commit/d28e166469228577f911f416e0d2f24b40e1dbc4))

## [0.1.1](https://github.com/sholdee/drydock/compare/v0.1.0...v0.1.1) (2026-05-26)


### Bug Fixes

* **deps:** align gitops-engine with argocd v3.4.2 ([24873ef](https://github.com/sholdee/drydock/commit/24873efea3cc203c311713ed11b0c39bfd783f34))
* **deps:** update module github.com/argoproj/argo-cd/v3 to v3.4.2 ([ecfc966](https://github.com/sholdee/drydock/commit/ecfc9668ac347830b493446daaee41a0aa01c771))
* **deps:** update module github.com/argoproj/argo-cd/v3 to v3.4.2 ([ad1d6bf](https://github.com/sholdee/drydock/commit/ad1d6bf8ee5ce7b121009b4ad47eddee7a9c36d0))
* **deps:** update module k8s.io/apimachinery to v0.36.1 ([0eff67e](https://github.com/sholdee/drydock/commit/0eff67e64bf2027ae17dc2178e13c78b9acd3276))
* **deps:** update module k8s.io/apimachinery to v0.36.1 ([e587a7a](https://github.com/sholdee/drydock/commit/e587a7a8392aa128706b6be5e282068318141cbc))

## 0.1.0 (2026-05-26)


### Features

* acquire git repositories ([ab01f70](https://github.com/sholdee/drydock/commit/ab01f70d003c5b7d07bf2475946fe8185908d61e))
* acquire remote kustomize resources ([565e4f8](https://github.com/sholdee/drydock/commit/565e4f862bec7988f2c17bce51ade440eaf1518b))
* add application test command ([5fe246d](https://github.com/sholdee/drydock/commit/5fe246ddd582ca9e28803255e47810c9bb0a071f))
* add appset provider fixture contract ([62e050b](https://github.com/sholdee/drydock/commit/62e050bc102a03832aa102cbf68da12741ac6f04))
* add authenticated source flags ([86f141a](https://github.com/sholdee/drydock/commit/86f141ac2992f6a4871b7dce4616f62f76ecdc14))
* add cache inventory operations ([ddfb611](https://github.com/sholdee/drydock/commit/ddfb61166da3a431e98ea97a262dddee22b4a400))
* add cache lifecycle cli ([49a15e0](https://github.com/sholdee/drydock/commit/49a15e00325267713e6eb715cd04a459deb7d530))
* add chart acquisition cache model ([122ad11](https://github.com/sholdee/drydock/commit/122ad112a7cbdea4eef7ee651fc0d81514876329))
* add deterministic render parallelism ([a7fb0a1](https://github.com/sholdee/drydock/commit/a7fb0a1642103911cd37eca3e25168c6d3e1c5a4))
* add diagnostics and manifest loading ([9f6013a](https://github.com/sholdee/drydock/commit/9f6013a6e236fca7af2b3572c7692bf2c2971c8c))
* add diff customization flags ([1ca9b3b](https://github.com/sholdee/drydock/commit/1ca9b3b6da05986057fde2bf519c1614797f748e))
* add local git ref snapshots ([b84f7ac](https://github.com/sholdee/drydock/commit/b84f7ac4c959b67acd548a1e61ad327ae4323b11))
* add lua health evaluator ([03ead16](https://github.com/sholdee/drydock/commit/03ead16a9e3add26ae320076382e9f5422e104e9))
* add named plugin registry ([eeb5cd6](https://github.com/sholdee/drydock/commit/eeb5cd652be1e9d469cb6d076a227a4e658c15c8))
* add offline appproject validation ([560af39](https://github.com/sholdee/drydock/commit/560af39ce91695c683b0f67ad11beb7bc45b5c80))
* add parent-aware diffs ([6d47d49](https://github.com/sholdee/drydock/commit/6d47d4937d9bd132fe3fa3de0d33ccdaacbaafd7))
* add plugin renderer timeout controls ([9be3a63](https://github.com/sholdee/drydock/commit/9be3a6366216656610a2f7e47710172479667b73))
* add public orchestrator api ([695f5fd](https://github.com/sholdee/drydock/commit/695f5fdff414f8ce3f83b3ab6371fd3d6db436bf))
* add redacted diag settings summary ([d07d1e5](https://github.com/sholdee/drydock/commit/d07d1e516e6537e76fb100f0ac7d0316c3bfc689))
* add remote resource cache ([0b4de4e](https://github.com/sholdee/drydock/commit/0b4de4edd016ea78b3a6e15b3b03b7a45c2fd65b))
* add rendered resource filters ([d4ebf92](https://github.com/sholdee/drydock/commit/d4ebf921ba6f5fa115835e1383c310ad03c607ff))
* add structured diagnostics ([f1c9b89](https://github.com/sholdee/drydock/commit/f1c9b897871eed1f72df80bdfcad52c4034e8ae1))
* add structured get output ([8be86a7](https://github.com/sholdee/drydock/commit/8be86a738ba0513ab51a911022f4ebadd8396357))
* apply global resource filters ([96ddd02](https://github.com/sholdee/drydock/commit/96ddd02e1c4849aa05ac584656c07206243ed55f))
* apply helm value files ([5f81723](https://github.com/sholdee/drydock/commit/5f817238d52f9ea4ffdd217aae7f4f4cbcfbb2ad))
* apply known type diff normalization ([993cc28](https://github.com/sholdee/drydock/commit/993cc28d654422e21fd31de913b14f9b7c41eb86))
* build generated applications ([f44b10e](https://github.com/sholdee/drydock/commit/f44b10ee57b9e5d10672c68d037f1e37bf20e60c))
* build one application ([e8daab3](https://github.com/sholdee/drydock/commit/e8daab3320af1a99cc6ea61d5f24401f205e59fb))
* combine rendered application sources ([2050830](https://github.com/sholdee/drydock/commit/20508306f61033d7125023f6a1cedf5e3b32b261))
* diff one application ([cbade5c](https://github.com/sholdee/drydock/commit/cbade5cbae051ce219b8ffc6d5763fee69f8a810))
* discover appprojects ([404bfbf](https://github.com/sholdee/drydock/commit/404bfbf4078f7009b5d56b12aff9bc7c03d1e679))
* discover Argo CD applications ([a2ec084](https://github.com/sholdee/drydock/commit/a2ec084a05348ba98f013ea2bdff227b03c30e1a))
* enable lua health test validation ([442591d](https://github.com/sholdee/drydock/commit/442591da8e8bda652214c509986e08265bdba822))
* expose appset provider fixtures in cli ([a08d10e](https://github.com/sholdee/drydock/commit/a08d10ec161f2e9eeaf2a07364eedb1ac1f8ea5e))
* expose cache acquisition events ([98b4546](https://github.com/sholdee/drydock/commit/98b4546ee0eb8c78ddeff092d3ae6bec3fb1f53d))
* expose git ref diff options ([3c38a0b](https://github.com/sholdee/drydock/commit/3c38a0b5af38cb11b57172c14c39a26f1d01635f))
* expose plugin renderer hook ([636e028](https://github.com/sholdee/drydock/commit/636e0286645c4cc55f16f2e563c9198651020e6b))
* expose redacted settings lua hashes ([c8bb30c](https://github.com/sholdee/drydock/commit/c8bb30c8c64aa6739f262cb2e6b85ed27f3e7990))
* expose render parallelism flag ([393a009](https://github.com/sholdee/drydock/commit/393a0097da380489c0855d66b6c118ad8dc8da95))
* fail closed for plugin sources ([596723e](https://github.com/sholdee/drydock/commit/596723ea8874d64b5b09d01473d01c216a7999a5))
* fetch http helm charts ([c67a8de](https://github.com/sholdee/drydock/commit/c67a8de285792544be1430995e11a3ae2dfce42a))
* fetch oci helm charts ([d893f45](https://github.com/sholdee/drydock/commit/d893f45701bc8d86270f033f61986dabb53723e2))
* generate git directory applicationsets ([976252b](https://github.com/sholdee/drydock/commit/976252ba513ca859b0ca7365a172b98d854e0172))
* honor application json pointer diff ignores ([c707cc5](https://github.com/sholdee/drydock/commit/c707cc5b4b6092e5712c12a6c6fe08bd8ff8a92a))
* honor applicationset selectors and generator templates ([6ee264d](https://github.com/sholdee/drydock/commit/6ee264d05afd01ee769b21ca6a2fc9bb66ea0da9))
* honor argocd compare options ([ee3dbe9](https://github.com/sholdee/drydock/commit/ee3dbe99ad3c265811e11651548704670aed2edf))
* honor global json pointer customizations ([743ea6f](https://github.com/sholdee/drydock/commit/743ea6f072681bb923eb1bb22fcf399a774b90b8))
* honor jq ignore expressions ([a734fd0](https://github.com/sholdee/drydock/commit/a734fd087e8fc1d84e5a71146bf016c8f589ddc5))
* honor managed fields ignore managers ([5e1df5a](https://github.com/sholdee/drydock/commit/5e1df5a3368dfa7b987c1c9d28919b9f8941edfe))
* ignore default helm diff noise ([232d28c](https://github.com/sholdee/drydock/commit/232d28c8b3c706c0265e393cb3313e2986e34ba3))
* load Argo CD settings ([8879223](https://github.com/sholdee/drydock/commit/8879223fe3c6313ef4f03b074f9ffb4ee21fab5d))
* map changed files to applications ([d36554a](https://github.com/sholdee/drydock/commit/d36554a14bf490f7e462874d4de66ac7abbf3f26))
* merge advanced resource customizations ([f79dd2d](https://github.com/sholdee/drydock/commit/f79dd2de6807dc382738ffbbd31105bb9bb1f9d7))
* parse advanced resource customizations ([7a02696](https://github.com/sholdee/drydock/commit/7a0269665e01f6b9b8772c543de802336bca45f6))
* parse argocd compare options ([cd1fa65](https://github.com/sholdee/drydock/commit/cd1fa65fe36902bb8eb1dea1f3b95daf552f64b6))
* parse global argocd diff settings ([4608f13](https://github.com/sholdee/drydock/commit/4608f1316eb2435a17f811bf8458f08c7c993388))
* parse kustomize remote refs ([01e755d](https://github.com/sholdee/drydock/commit/01e755d377806270a3297504980f786e708820ed))
* plan application sources ([48834e6](https://github.com/sholdee/drydock/commit/48834e6890af93120a1f02106bf0b16beaf70577))
* plumb git source flags ([fd3b0d8](https://github.com/sholdee/drydock/commit/fd3b0d8ec358dab000e707eafbc73242759361a0))
* plumb remote kustomize credentials ([f22670e](https://github.com/sholdee/drydock/commit/f22670e13f623796049e75e450e03e84535ca412))
* prepare remote kustomize graph ([d96231c](https://github.com/sholdee/drydock/commit/d96231c0fee2778da38635ee9dff08b7ac6cc4fe))
* render chart-only applications ([2b50cdf](https://github.com/sholdee/drydock/commit/2b50cdf68cf9b57795eb5f0a968e9c24d6fff9aa))
* render directory manifests ([93651a3](https://github.com/sholdee/drydock/commit/93651a3c372f210a97f185046ba39ed96fc1c5f4))
* render helm sources ([3e7dae5](https://github.com/sholdee/drydock/commit/3e7dae5057b5cf2221faf7a7ee50046746fe7be0))
* render kustomize helm charts locally ([80ef5f8](https://github.com/sholdee/drydock/commit/80ef5f84161fb2af7ea39d76a201a24012041bff))
* render kustomize sources ([fe62cf6](https://github.com/sholdee/drydock/commit/fe62cf6089b2c204b98a101d2fc149412dde29dd))
* render remote kustomize file resources ([ae41884](https://github.com/sholdee/drydock/commit/ae41884d3db5c7058230c48675a65c9653172ea1))
* resolve git source roots ([332629b](https://github.com/sholdee/drydock/commit/332629be0bdb7d2f2a00265dff5a05f67affbb1a))
* resolve repository maps ([257f464](https://github.com/sholdee/drydock/commit/257f46408d79bc1628fa41f26a0b123bc02dda69))
* retain health lua settings source ([f899113](https://github.com/sholdee/drydock/commit/f8991131e5a0d506b77140a14ebcae8f3f76dd26))
* rewrite remote kustomize path refs ([9e1c2db](https://github.com/sholdee/drydock/commit/9e1c2db767b1cad484aaf09dad04a6c785f0bb67))
* route appset provider options ([d96b26e](https://github.com/sholdee/drydock/commit/d96b26e19b52eb5fe1ae4e6103c999b603bd090e))
* select applications by name ([0eef2ed](https://github.com/sholdee/drydock/commit/0eef2ed855a46818bd3fb9dfa7005732eaecc694))
* select applications from changed paths ([45eb58b](https://github.com/sholdee/drydock/commit/45eb58bdfd853defd165cc2383db7e28e4bbe0ec))
* stream test app status output ([99b0f82](https://github.com/sholdee/drydock/commit/99b0f8269ee169cf390b43081eaf558c22c17483))
* support applicationset matrix generator ([b977038](https://github.com/sholdee/drydock/commit/b977038644230f45e8c3af5c5b9e895702d73805))
* support applicationset merge generator ([49f5486](https://github.com/sholdee/drydock/commit/49f54860752248ba21efb1a9051009d4202270eb))
* support fixture-backed appset plugins ([cfdb3ea](https://github.com/sholdee/drydock/commit/cfdb3eaefe340f75abdfd7759b41873822238d93))
* support fixture-backed cluster appsets ([cb97e53](https://github.com/sholdee/drydock/commit/cb97e53cfa44ce28c3365c51384aea528adcfd6f))
* support fixture-backed scm appsets ([506f972](https://github.com/sholdee/drydock/commit/506f972d940d60141b1ebfd7fa70a08a73560f20))
* support kustomize load restrictor option ([43c7232](https://github.com/sholdee/drydock/commit/43c7232cb61d6445092d1d3682c0bb82055ca3e6))
* support more applicationset generators ([3719b38](https://github.com/sholdee/drydock/commit/3719b3894be8e464422a33d13b37705154dc03cf))
* validate lua health in app tests ([90027f6](https://github.com/sholdee/drydock/commit/90027f65d39038796743e3566e57060d18a3b218))
* wire appproject validation into builds ([929a16d](https://github.com/sholdee/drydock/commit/929a16df9dba813947fa85c10622403092152b94))
* wire chart cache flags ([d514c3b](https://github.com/sholdee/drydock/commit/d514c3bce32c7aeb8eea957a30af35fcbb79a2f1))
* wire CLI commands ([d039c06](https://github.com/sholdee/drydock/commit/d039c06fcd3d4e20ca5c6150f44f2063c52bf7b5))
* wire diff apps ([fe71ab7](https://github.com/sholdee/drydock/commit/fe71ab728c6b80e69a5c85472a52e29ff1dcffa5))
* wire diff images ([e2ebaff](https://github.com/sholdee/drydock/commit/e2ebaff12621930dac85c095326705ea17b8ebc4))
* wire git refs into app diffs ([19f2254](https://github.com/sholdee/drydock/commit/19f2254418d82be72ed59658b7c48c3bb0669523))
* wire named app commands ([e70de9b](https://github.com/sholdee/drydock/commit/e70de9b871366597ee51cf926d9d3a0649977f1e))
* wire remote resource options ([70506eb](https://github.com/sholdee/drydock/commit/70506eb760c46c4abede20b7d311a8d875f74eb2))
* wire repository diagnostics ([37426f6](https://github.com/sholdee/drydock/commit/37426f6d5878da7265e9c27e19fa4d9b278a77c0))
* write acquisition cache metadata ([de1eef4](https://github.com/sholdee/drydock/commit/de1eef44705a5b26df1350e4467f8064455ad1db))


### Bug Fixes

* align applicationset rendering with argocd ([fc33c97](https://github.com/sholdee/drydock/commit/fc33c9710adf96671042b3197904fd772b0df042))
* align diff API with task contract ([cc32159](https://github.com/sholdee/drydock/commit/cc32159163fb10d72fc39f05d2bc8ea598ac8716))
* align git source parity semantics ([8818e33](https://github.com/sholdee/drydock/commit/8818e332e1fccb5d0d1cc81967663ee2fbc8fc26))
* align kustomize helm chart semantics ([ba78c4b](https://github.com/sholdee/drydock/commit/ba78c4b32ac83e748c86ac85faed12744c70b328))
* align labops render parity gaps ([5ef9dd0](https://github.com/sholdee/drydock/commit/5ef9dd04b8525a5970482bb040735386a8cd0677))
* align remote source acquisition with argocd ([7eee058](https://github.com/sholdee/drydock/commit/7eee05809cc7411b3bdca932c9ab9800fba2cd47))
* allow cached oci charts without puller ([5cf9f2d](https://github.com/sholdee/drydock/commit/5cf9f2dc75ee2195d9c084201d553806c725b688))
* allow repo-bounded kustomize helm values ([0405dab](https://github.com/sholdee/drydock/commit/0405dab4e3bf7fa605f7e9f5c16dc92a285be1fd))
* allow repo-local kustomize components ([a61cc3d](https://github.com/sholdee/drydock/commit/a61cc3dcb9358fea1caa100b8b11aaf06317ebb0))
* anchor same repo value refs ([03947de](https://github.com/sholdee/drydock/commit/03947de67db6a6ad7a0ae08ceedf79a6f1d4a363))
* apply discovered kustomize build options ([4298bd9](https://github.com/sholdee/drydock/commit/4298bd9339b5a9a5340c910028e4d59c55852d86))
* canonicalize cache metadata identities ([bbf49ee](https://github.com/sholdee/drydock/commit/bbf49ee65003a482cd141a22c5d4645e1e62fc4c))
* clarify directory and source path diagnostics ([8d9fb4c](https://github.com/sholdee/drydock/commit/8d9fb4c76d66e04b0cae30e4e79520d234848e47))
* clean up plugin public api lint ([d8bd09c](https://github.com/sholdee/drydock/commit/d8bd09cbd681821dc0a5ffc6dfe21cfde0839e2c))
* close helm value file spec gaps ([c863c46](https://github.com/sholdee/drydock/commit/c863c465a6354ae2c705fd692c14fa047d81d2a3))
* color test app status output ([116c0b7](https://github.com/sholdee/drydock/commit/116c0b74d0388fc2d5c906257d8f330470498473))
* constrain diff identity and image extraction ([c19800d](https://github.com/sholdee/drydock/commit/c19800dee1b66991e106670dc4a4219d3835bc13))
* contain directory renderer paths ([08ca876](https://github.com/sholdee/drydock/commit/08ca876de43589c593abb9e5700ea33978102d65))
* **deps:** update github.com/argoproj/argo-cd/gitops-engine digest to 21ea32e ([8164d59](https://github.com/sholdee/drydock/commit/8164d59755b5d2a6349f218bc57c744627d2c25a))
* **deps:** update github.com/argoproj/argo-cd/gitops-engine digest to 21ea32e ([e682b78](https://github.com/sholdee/drydock/commit/e682b785389ba87f6a351726d44e52804d55c85f))
* dispatch local build renderers ([8f097cb](https://github.com/sholdee/drydock/commit/8f097cb87a002aa84e170b1d49e540a581643267))
* expand built-in cluster scope checks ([96e0e7f](https://github.com/sholdee/drydock/commit/96e0e7f9d8271064d54001fa377365afc78bdf1f))
* fail closed on cross-repo helm refs ([10e2047](https://github.com/sholdee/drydock/commit/10e2047ec63f1ef0f247f8bc4aed4aee7d8541b1))
* fail closed on duplicate customization bodies ([f801e9a](https://github.com/sholdee/drydock/commit/f801e9ab529cfa15f713f534c7d8d13bd841d47b))
* gate diag cache events ([c7e11a2](https://github.com/sholdee/drydock/commit/c7e11a2feba59d4ef4c7048d8cf154135781e3db))
* harden application rendering orchestration ([710b5d6](https://github.com/sholdee/drydock/commit/710b5d6ec2fc48a06ed142b881d37fdc4e22dfab))
* harden appset provider parity ([b276107](https://github.com/sholdee/drydock/commit/b276107e4af7fa86429546a5230074d161380ed6))
* harden cache delete cli validation ([0709997](https://github.com/sholdee/drydock/commit/0709997af0579687c65bd5940fff0603502d9aac))
* harden cache entry recognition ([a52a925](https://github.com/sholdee/drydock/commit/a52a92527f597d0f8fcd156b443b712e48d7ea91))
* harden cache event redaction ([91e19e9](https://github.com/sholdee/drydock/commit/91e19e915dbc55f65faad4da391286d0e93c3fdc))
* harden changed path detection ([c10d3e8](https://github.com/sholdee/drydock/commit/c10d3e80cf5a4ced6b4e6dc05b2a8bf1ccc5db4d))
* harden chart repository normalization ([f374d2c](https://github.com/sholdee/drydock/commit/f374d2c04a767bf601c645b9341b12a0ad2352f1))
* harden discovery scanning ([e0f0af5](https://github.com/sholdee/drydock/commit/e0f0af563d692d6d4eac369b5406d15f93b8a19f))
* harden git ref diff outputs ([de2d9a0](https://github.com/sholdee/drydock/commit/de2d9a0cb3d2756b492c89c493447fbbba69206c))
* harden git source fetching ([99352dd](https://github.com/sholdee/drydock/commit/99352dda60f02214cd8b4cbce6ea6bc5a54198c6))
* harden helm rendering ([83cfd7e](https://github.com/sholdee/drydock/commit/83cfd7e30bf6563b5662840be3f20e5aeec2c1d9))
* harden helm value file rendering ([7e9a258](https://github.com/sholdee/drydock/commit/7e9a258f08d9a5046ada46fb03386f64167fe59d))
* harden http chart cache writes ([9482e2c](https://github.com/sholdee/drydock/commit/9482e2c6c2341b05bfead20dbcd3972d7809710d))
* harden kustomize helm chart rendering ([63506e9](https://github.com/sholdee/drydock/commit/63506e9de9389283392f40c183073ce270680b93))
* harden kustomize rendering ([35fb767](https://github.com/sholdee/drydock/commit/35fb767827bfe73f448dad97790fec506e78c75c))
* harden remote cache safety ([a7e11bf](https://github.com/sholdee/drydock/commit/a7e11bf084195a0979af1b1e8b94a44f5a318598))
* harden remote kustomize resources ([a680e3e](https://github.com/sholdee/drydock/commit/a680e3e5a27190faad57dc3d1cf8b711b8e63dcb))
* harden secret diff redaction ([efaae95](https://github.com/sholdee/drydock/commit/efaae956a615a13d13e0f678cb9f0329089591a8))
* hide cache metadata from render roots ([8e221e6](https://github.com/sholdee/drydock/commit/8e221e65134826a81ab9ea8d620a47789c256bbb))
* honor project-scoped repositories in validation ([d770ed1](https://github.com/sholdee/drydock/commit/d770ed16e104a441bb11665af5dd3b75b979d2f2))
* improve repository settings and helm rendering ([d93eaf0](https://github.com/sholdee/drydock/commit/d93eaf0d1c4082c7249b16b1406ff6618e89928a))
* include application manifests in changed-only diff ([5227013](https://github.com/sholdee/drydock/commit/5227013f744fa72a980978c47831f1668d5cd07c))
* include helm chart default values ([8bb722f](https://github.com/sholdee/drydock/commit/8bb722fc134d3c738ec2dbcb9879f931b4ac2bfe))
* include helm value files in changed-only diff ([d51920f](https://github.com/sholdee/drydock/commit/d51920f0a416a8cf0ca2c379af2bd9ccacf2d8b8))
* include plugin request context ([ee03506](https://github.com/sholdee/drydock/commit/ee0350645b5b54bec7e56b8adc488b9c48ba8f9f))
* include symlink path changes ([551ca29](https://github.com/sholdee/drydock/commit/551ca292cd4ec0bf147ab5d2abf8e7dda52a339b))
* isolate oci registry credentials ([52adaaf](https://github.com/sholdee/drydock/commit/52adaaf89227b86c2501dbacb571e0c0be84b30a))
* keep app listing render-free ([0a60d57](https://github.com/sholdee/drydock/commit/0a60d572be9d47f09a6e3775c00f4317e2fe593d))
* make remote git cache identity idempotent ([2251d75](https://github.com/sholdee/drydock/commit/2251d753898b75c3e3e93601d79ef99be57f928c))
* map aliased helm subchart paths ([4225f9e](https://github.com/sholdee/drydock/commit/4225f9e03882e77ad3d1725db8a2f806ca925c72))
* map helm index auth failures ([c72701d](https://github.com/sholdee/drydock/commit/c72701d3c94342768bfac56e23971df33e3bf72f))
* map remote aliased helm subchart paths ([eb49857](https://github.com/sholdee/drydock/commit/eb498576c44925efe46fe00b275d100133431bfe))
* map repeated helm alias paths ([451e997](https://github.com/sholdee/drydock/commit/451e997213399251a9c58dff6f74ec8bb5797d46))
* match kustomize local chart layout ([ce2b04f](https://github.com/sholdee/drydock/commit/ce2b04fa13213147d3f15209e3850129838beb14))
* preserve build app inputs ([d37999d](https://github.com/sholdee/drydock/commit/d37999d2b5dbaa70573d7a172ff5057ca7017f08))
* preserve helm manifest paths ([6693cea](https://github.com/sholdee/drydock/commit/6693cea56e7e592f3e0de9f5d30f1e4a485e001f))
* preserve helm valuesObject precedence ([d7ed223](https://github.com/sholdee/drydock/commit/d7ed2231edce756f872aced725c80bab606e3f6f))
* preserve kustomize helm values precedence ([0c66f39](https://github.com/sholdee/drydock/commit/0c66f3908bd4b5622a33566aa2e1ca21d6dec591))
* preserve plugin partial diff results ([cea1392](https://github.com/sholdee/drydock/commit/cea139244117b720da1dde7a64cf464e269ee98f))
* print diff diagnostics on errors ([eb68a74](https://github.com/sholdee/drydock/commit/eb68a74bee8518849520848c74fbb9295834c7db))
* print non-strict diagnostics ([8dff621](https://github.com/sholdee/drydock/commit/8dff621cf55b5092e7feaaa0bfe46e985365eb63))
* protect cache roots with baseline path ([9045193](https://github.com/sholdee/drydock/commit/90451932fec6adc9c7d8621d365cef016e73e415))
* redact cache query values ([f1f9a03](https://github.com/sholdee/drydock/commit/f1f9a03f36034673983e2ece26016edf03bbd02a))
* redact encoded cache query values ([92b8e10](https://github.com/sholdee/drydock/commit/92b8e105939825fe3a417777cc511123fdb56b24))
* redact helm ref repository errors ([ebcb6bf](https://github.com/sholdee/drydock/commit/ebcb6bf553b3cdf46cc4c5137c88359e7d87bee1))
* redact invalid oci repository errors ([22dff0d](https://github.com/sholdee/drydock/commit/22dff0d5a9d22b7f8c0e487d0c1101d69da47d2d))
* redact secret diffs ([bc3a3c9](https://github.com/sholdee/drydock/commit/bc3a3c96d053cba69b0e297344978b0052a1a1ed))
* reduce diagnostic code complexity ([d92a70f](https://github.com/sholdee/drydock/commit/d92a70fdc1b09fb61415505e2c8e09c2e4d6f0ba))
* reduce live test output noise ([3672a24](https://github.com/sholdee/drydock/commit/3672a24a3f4d2104f17b0936660f94ea835d0c0b))
* reduce named cluster project warning noise ([32d380e](https://github.com/sholdee/drydock/commit/32d380ef9e545117e1f3ca9f94b2242f52d1cd04))
* reject colon-style kustomize refs ([c2d21e2](https://github.com/sholdee/drydock/commit/c2d21e2afa0525ad307052949598b16477608c82))
* reject symlinked app manifest parents ([60d5d1f](https://github.com/sholdee/drydock/commit/60d5d1f521efdf37b95ed1cbc6278cfcee27847b))
* reject symlinked remote cache keys ([114d7bf](https://github.com/sholdee/drydock/commit/114d7bf7c6b91d4447dd7228b31f641839759803))
* reject version command operands ([93d5419](https://github.com/sholdee/drydock/commit/93d5419e70adecc27e7973c4953dadf9be2dc8cc))
* require source resolution for external value refs ([eb873a3](https://github.com/sholdee/drydock/commit/eb873a325fc0b76363aa9e7fbd7a63ff170ca5db))
* satisfy benchmark lint ([9d46250](https://github.com/sholdee/drydock/commit/9d4625085403db3364c849ab624b0f6a76090bdb))
* satisfy final lint checks ([68a27bf](https://github.com/sholdee/drydock/commit/68a27bfdb36cc32edeb87b4fe7c9e0895d52a1cc))
* satisfy lint configuration ([c775e2c](https://github.com/sholdee/drydock/commit/c775e2c2c51159b8492f8c1c93e9dff28c74ac65))
* satisfy secret diff lint ([b228c94](https://github.com/sholdee/drydock/commit/b228c94950a2ee009b4a4533d365ca642e19a514))
* scope lua compile diagnostics to matching apps ([942afd7](https://github.com/sholdee/drydock/commit/942afd7714cd82af4e948256557b2dd1dfc5a072))
* skip cluster-scoped namespace normalization ([3211c5b](https://github.com/sholdee/drydock/commit/3211c5b03f6315d9d544f0dbbf2c317666e911d6))
* skip helm templates during discovery ([498910b](https://github.com/sholdee/drydock/commit/498910b0449be81b21809401c1e24a12803985f0))
* skip unsupported appsets in non-strict mode ([b2e5aab](https://github.com/sholdee/drydock/commit/b2e5aabf475208b53f0c23cdf761a0fc5544fb8d))
* support redacted scp cache aliases ([bf3eb2d](https://github.com/sholdee/drydock/commit/bf3eb2d2a7e0f18347b8d6daf7bde6c262d929fd))
* suppress lua health stdout ([77154dc](https://github.com/sholdee/drydock/commit/77154dcfc1c626df07efdae3ea1fd10eb59ff601))
* tighten http chart cache validation ([9c57f7f](https://github.com/sholdee/drydock/commit/9c57f7fa8cc2e54a108975c038b41a290f120d5f))
* use digest separator for OCI chart refs ([8179d13](https://github.com/sholdee/drydock/commit/8179d13fa7e50493220a211b9cfa20f53ed365b6))
* validate cache path safety roots ([4cef700](https://github.com/sholdee/drydock/commit/4cef70034a3b4e99dcafbd0b4532ebefc11dafbf))
* validate directory list wrappers ([c207ff7](https://github.com/sholdee/drydock/commit/c207ff72bf3e4467975954484f7697e2fea4de31))
* validate empty directory list wrappers ([848056b](https://github.com/sholdee/drydock/commit/848056b27526ca79a4186da1a0102d6ebb9af6fe))
* validate kustomize path fields ([1898929](https://github.com/sholdee/drydock/commit/18989291a7bf32d8203b1a7491997afd88ae3135))
* validate nested applicationset generator structure ([7baec3f](https://github.com/sholdee/drydock/commit/7baec3f8736badf536e028ec0315aaa2909ee474))
* validate nested applicationset generator support ([57afbaa](https://github.com/sholdee/drydock/commit/57afbaa1c31fa7e6b1214637b9e03293226c79f5))
* validate oci repositories before cache lookup ([f97e737](https://github.com/sholdee/drydock/commit/f97e73720a3f9e01b7e4a0cf62579798b22cb1b9))
* validate repository secret oci flag ([0bfff1c](https://github.com/sholdee/drydock/commit/0bfff1c9fe9a1ac646a93b86e16f2d748d741ff8))
* wire helm ref render options ([9f5a693](https://github.com/sholdee/drydock/commit/9f5a69360abd98a2223b1591355c0b85bae5c95f))
* write metadata on cache hits ([71b7df3](https://github.com/sholdee/drydock/commit/71b7df35b9e4cc13d8bc8c9326e1535c04ef20f5))


### Performance Improvements

* avoid full remote git render snapshots ([f2907e6](https://github.com/sholdee/drydock/commit/f2907e69b951a18c9aa0fd1fd2b7839959f1ccdf))
* avoid full repo copies for kustomize prep ([f50077b](https://github.com/sholdee/drydock/commit/f50077b12b0e9e050c14880852a6f50664e498e2))
* improve source snapshot handling ([ffde282](https://github.com/sholdee/drydock/commit/ffde282226b5d6daf86fab9eda0ee44654d7a45e))
* reduce test apps cache acquisition overhead ([3d42c01](https://github.com/sholdee/drydock/commit/3d42c01e3151a428b642874ee35c93fe36cce5ab))
* skip manifest retention for test validation ([09b32e3](https://github.com/sholdee/drydock/commit/09b32e384c0fec1d3566107d40568f3956a7d063))
* speed up git ref diffs ([0d94229](https://github.com/sholdee/drydock/commit/0d942294b6ad000612a3bc62755d8015c8a22dba))


### Documentation

* add agent orientation roadmap ([3fb08a4](https://github.com/sholdee/drydock/commit/3fb08a4fd31c8edbd397d07d6f5e8154b8053deb))
* add documentation ownership index ([e226019](https://github.com/sholdee/drydock/commit/e226019f26f43b7564e09319350d55bdcc514c9d))
* add drydock logos ([01f50db](https://github.com/sholdee/drydock/commit/01f50db4352b8c14de257d9c76d407dd27115116))
* add phase 1b cache lifecycle plan ([eea4658](https://github.com/sholdee/drydock/commit/eea4658b212cae3d602e0aba7a124d9d743df731))
* add readme badges and workflow diagram ([7e8b3cc](https://github.com/sholdee/drydock/commit/7e8b3ccf81915133b2959f4cb9b96ce43ec7baf8))
* align phase 2 support boundaries ([e34f3b4](https://github.com/sholdee/drydock/commit/e34f3b46c6ec3833eec6c1118d4314b910ee7838))
* clarify cache lifecycle boundaries ([f4421c1](https://github.com/sholdee/drydock/commit/f4421c1fc6a36f12d74a497a443cc792c037696f))
* clarify deferred health scope ([296b56f](https://github.com/sholdee/drydock/commit/296b56f9aa232ba1cff992bec8fea1c93231a5c8))
* clarify remote kustomize ref shapes ([310be0c](https://github.com/sholdee/drydock/commit/310be0c5c2350181877c975fc3a7c8a38e9132ab))
* clarify wired MVP commands ([1e3075a](https://github.com/sholdee/drydock/commit/1e3075a51c74a8f141f458e3705154e7595821ac))
* distill agent operating guide ([7ba6414](https://github.com/sholdee/drydock/commit/7ba641403b2822e1fb267fa711b8ec297af9ce1e))
* document advanced settings parity ([084f627](https://github.com/sholdee/drydock/commit/084f627ec97427fe7db36f9e244ce4fae1a7330f))
* document applicationset generator parity ([bfaf98a](https://github.com/sholdee/drydock/commit/bfaf98a0fe1b71bdbb6d7e9eb58300f92a1cf146))
* document cache lifecycle commands ([b5677fd](https://github.com/sholdee/drydock/commit/b5677fda95cc44de43789fbfa9e4a4e4f8fc1947))
* document chart diff flags ([13d4d99](https://github.com/sholdee/drydock/commit/13d4d99574dfc254fcf77d52664921a32088efa2))
* document chart diff workflow ([4b951f3](https://github.com/sholdee/drydock/commit/4b951f38ef052f27d7bb5a5e7bc0a96a7098f2c7))
* document ci and release hardening ([8fe6af8](https://github.com/sholdee/drydock/commit/8fe6af89e2da510daee678db4b627fcbfa46ad8b))
* document git ref diffs ([5df5fe4](https://github.com/sholdee/drydock/commit/5df5fe45df9de66f31acd6bd42728353dea545d3))
* document git source fetching ([dd8ba87](https://github.com/sholdee/drydock/commit/dd8ba8707bfab7d6baccb73257cfa96faad5f770))
* document lua health validation ([9b1105e](https://github.com/sholdee/drydock/commit/9b1105eb2cd6e9dd1498bbe7632fd224b98757a9))
* document MVP usage and compatibility ([5caf350](https://github.com/sholdee/drydock/commit/5caf35030521275afae3f0769c07d9918324025d))
* document named app commands ([b22178a](https://github.com/sholdee/drydock/commit/b22178a9e0e9da46966fc3655f35b82c49d4035d))
* document offline hardening workflow ([e34d5d9](https://github.com/sholdee/drydock/commit/e34d5d963dae25dfe65ee82c35396e94b25115e7))
* document plugin renderer contract ([828d44a](https://github.com/sholdee/drydock/commit/828d44a309af84b491affa32b1ee4716397e91a3))
* document plugin source strategy ([eb1d125](https://github.com/sholdee/drydock/commit/eb1d1252f6e798928a221e0aaa8a8945d5ee464b))
* document project validation parity ([8c86a5f](https://github.com/sholdee/drydock/commit/8c86a5fd7428d1646ef4276bad2f8b022c161350))
* document redacted settings diagnostics ([9b8edd1](https://github.com/sholdee/drydock/commit/9b8edd1c042b4d587c8af50ac9ed9e56de684150))
* document remote kustomize resources ([41915a1](https://github.com/sholdee/drydock/commit/41915a17bbacbe1244932f2bbe50a0fcfab06eee))
* document source and kustomize parity ([d912691](https://github.com/sholdee/drydock/commit/d912691fa6162b3234f1789bf298594c4f86270b))
* finalize documentation surface audit ([421b7a5](https://github.com/sholdee/drydock/commit/421b7a52b62c26179f6da39dd418d7a85b49515a))
* inventory home-ops app patterns ([3b4d2c8](https://github.com/sholdee/drydock/commit/3b4d2c8b2190b10d28e7fdf9b339dc4e6f00be41))
* keep agent escalation nonblocking ([e43dc83](https://github.com/sholdee/drydock/commit/e43dc83345a5911c413c78a850a4fa4e373e2dff))
* link live integration boundary ([ca2d861](https://github.com/sholdee/drydock/commit/ca2d861ab051af99dd153f2fae70e2f2cf44fe9f))
* mark pattern smoke as planned ([6bcece8](https://github.com/sholdee/drydock/commit/6bcece81775e36c5ba05abd3aa4f2f8992959dff))
* plan cli api ci hardening phase ([5a7f0f9](https://github.com/sholdee/drydock/commit/5a7f0f9df29046215385fa9787215e26a8d01422))
* plan validation policy parity phase ([d80c1ec](https://github.com/sholdee/drydock/commit/d80c1ecc0df805f09176279f56b04ea705aacde4))
* prepare public setup action ([96501e5](https://github.com/sholdee/drydock/commit/96501e51938032e89c07ce9c66ca8cb7919607e4))
* promote design and roadmap docs ([6d6e6c5](https://github.com/sholdee/drydock/commit/6d6e6c555c54e2c7a6802f5f151a7ce0eb1dc907))
* prune implemented plans ([4db5442](https://github.com/sholdee/drydock/commit/4db5442de563802beac91827f131aa9835871243))
* prune redundant documentation ([5a209b2](https://github.com/sholdee/drydock/commit/5a209b22fb1cedb7886a4849854174f1661547aa))
* prune stale reports ([5452da2](https://github.com/sholdee/drydock/commit/5452da2ad1d054cd1c67a9c9a2f44e137255fa69))
* record r3 refactor remediation ([77cc996](https://github.com/sholdee/drydock/commit/77cc9960762d87a4534562706914498ab4c5cc46))
* record r4 refactor remediation ([6877a20](https://github.com/sholdee/drydock/commit/6877a2025dc8a2e93f2571023d8c677df7d2290d))
* reinforce live integration design gate ([4cb14df](https://github.com/sholdee/drydock/commit/4cb14df0056d7606033d2618cdedc18295da31cf))
* rename project references to drydock ([6be8831](https://github.com/sholdee/drydock/commit/6be8831924a3673a84c84b835260b650504427d9))
* report offline diff compare semantics ([9553a88](https://github.com/sholdee/drydock/commit/9553a88c79a95999ef838c617b2648a0dfcdc841))
* report outstanding argocd-local work ([700a909](https://github.com/sholdee/drydock/commit/700a909b0ec674b83c7fa9eaa07215419b610124))
* rewrite operator readme ([da5de21](https://github.com/sholdee/drydock/commit/da5de214f788d606c50da59cefdc39235842edaa))
* summarize argocd-local state ([2fc505c](https://github.com/sholdee/drydock/commit/2fc505cdd817bb2a0ee49b8e66a2aacb8bfed642))
* trim duplicate documentation detail ([f1dba73](https://github.com/sholdee/drydock/commit/f1dba73b0e147dbe67a543caf3da4c64fc2d6746))
* update audit remediation compatibility ([6ada0dc](https://github.com/sholdee/drydock/commit/6ada0dc287281b5441fb0c2a3034b1d6e4bfae7a))
* update live guidance for drydock identity ([c18e61c](https://github.com/sholdee/drydock/commit/c18e61c3c61d1416a66be8f57336822308cde3a0))
* update next-step support contract ([50d045b](https://github.com/sholdee/drydock/commit/50d045bfb2f6e7971aa21e6d6a216f1f6a65d7cd))


### Miscellaneous Chores

* **deps:** update dependency shinagawa-web/gomarklint to v3.2.0 ([741b90c](https://github.com/sholdee/drydock/commit/741b90c07b32ca4be3b612b083be85c47335e206))
* **deps:** update dependency shinagawa-web/gomarklint to v3.2.0 ([43ccb42](https://github.com/sholdee/drydock/commit/43ccb420d2c765e8f0013e74a6c4fbcd97e4758a))
* finalize git source fetching ([98d2e2f](https://github.com/sholdee/drydock/commit/98d2e2fa12ccbf0d5e045bf95725fc59924479ee))
* fix named app lint ([8307253](https://github.com/sholdee/drydock/commit/8307253c7c54c8a0abdb8b892227821aec660e8f))
* ignore local worktrees ([a317129](https://github.com/sholdee/drydock/commit/a3171295f5e5ab06518f1cf08a7241d0c9cab982))
* prepare release tooling ([7015928](https://github.com/sholdee/drydock/commit/7015928fadd9d4a67c1f471ab8e805d329c11c00))
* prune docs and use go markdown lint ([4cd9cb6](https://github.com/sholdee/drydock/commit/4cd9cb679fa3a17c24c0b820667ce02d1fbc7bc1))
* rename cli and cache namespace to drydock ([7aee485](https://github.com/sholdee/drydock/commit/7aee4857a99287f38a36df1bba81277ad489335a))
* rename module and public package to drydock ([617a4dd](https://github.com/sholdee/drydock/commit/617a4dd5e9d829315aa6ab51f36ad82c82cbfb30))
* scaffold argocd-local ([b0cf459](https://github.com/sholdee/drydock/commit/b0cf459598a9b91712f08facab1b275162c8deb2))


### Code Refactoring

* centralize cache event helpers ([7f1623f](https://github.com/sholdee/drydock/commit/7f1623fac7d865015c955020b2de72d9ef0bf7f8))
* centralize cli output parsing ([cd3c3cf](https://github.com/sholdee/drydock/commit/cd3c3cfd136b87584f015a2ad781c86730b98af1))
* centralize request option construction ([ab5dd6b](https://github.com/sholdee/drydock/commit/ab5dd6b821a32905af3dbb575c65f2cd2ed6b20d))
* extract acquisition session ([7ea42df](https://github.com/sholdee/drydock/commit/7ea42df43b78956cdf7a98111cd1aa1ee70e0699))
* extract build session ([8f5ead0](https://github.com/sholdee/drydock/commit/8f5ead004f740c2572cee26e5ab02ab56fea1a94))
* extract kustomize workspace preparation ([c78ebab](https://github.com/sholdee/drydock/commit/c78ebabfbc1025a8c059c6b5edf0343a15614dae))
* normalize path safety primitives ([a41e19b](https://github.com/sholdee/drydock/commit/a41e19b3fd1a49567601b95b945a555678bda87f))
* reduce settings parser complexity ([2d31e95](https://github.com/sholdee/drydock/commit/2d31e95e16a5d0bbff2226054882948eac0b3018))
* simplify git acquisition flow ([f6530a3](https://github.com/sholdee/drydock/commit/f6530a3ffbf0a4c4bd08aacf667f653907e5642e))
* split app local provider ([38cfeac](https://github.com/sholdee/drydock/commit/38cfeace1192c4495c6789667d78478c0769f68c))
* split applicationset generator families ([928468b](https://github.com/sholdee/drydock/commit/928468b53b01e9d248955218ba58aea4faa4cee9))
* split appset provider generators ([84964b6](https://github.com/sholdee/drydock/commit/84964b61a080340f9f8ac8b1b0b9fdd7e1206b2a))
* split chart repository adapter ([b9ee6dd](https://github.com/sholdee/drydock/commit/b9ee6dd116e247f9e2b31349737967e1c3ca18d6))
* split diff and settings grammar ([2d9d00b](https://github.com/sholdee/drydock/commit/2d9d00baa6ba901c05022987c45543ebf775cc1c))
* split kustomize workspace helpers ([7730ad5](https://github.com/sholdee/drydock/commit/7730ad58f2fd2793a7f42cefbe716e1c31154a45))
* split public api implementation ([4bd6815](https://github.com/sholdee/drydock/commit/4bd6815b0a5fdeac5bc51a3c6acee27b377fd803))

## Changelog

All notable changes to this project will be documented in this file.

This project uses [release-please](https://github.com/googleapis/release-please)
to maintain the changelog and create SemVer releases.
