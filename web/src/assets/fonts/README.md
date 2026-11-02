# Bundled fonts

IBM Plex Sans Arabic, Regular and SemiBold, as WOFF2, unchanged, under the SIL Open Font License 1.1 (`LICENSE.txt`).

From npm `@ibm/plex-sans-arabic@1.1.0`, verified before extracting:

- integrity `sha512-u8wIS6szLAOFvlBjCFZmtpKIqbhuIuniG2N0J+sio8vV6INH58hP0t0QNYrSl9SZtCv2Fwb4oQGuZJY3kJ4+QA==`
- tarball sha256 `71ad7bc2d0cacebb1dfa7065603dfa0287b2b90c7c56023344e1da82b1766ddb`

Files are copied in, so the package is not a dependency.

IBM Plex Sans, Regular and SemiBold, subset to Latin with fontTools 4.57.0 (the `.subset.woff2` files), under the same licence.

From npm `@ibm/plex-sans@1.1.0`, verified before extracting:

- integrity `sha512-WPgvO6Yfj2w5YbhyAr1tv95RUz4LRJlqN+CmYvBglabXteufP1D1E9BABMde+ZIKdRbFJDoKF5eQzfhpnbgZcQ==`
- tarball sha256 `c3818979c2a2c82927ea3e7c485e389a18de0b7462887dd4f7acc9a525568962`

JetBrains Mono Regular, subset the same way, without code ligatures, under the SIL Open Font License 1.1 (`LICENSE-JetBrainsMono.txt`).

From `JetBrains/JetBrainsMono` tag `v2.304` (`cd5227bd`), `fonts/ttf/JetBrainsMono-Regular.ttf`. The release publishes no hash, so the file was checked against its git blob, `dff66cc50702c75abd025dcf49f62a4dcc2d72de`.

The subset is Basic Latin, Latin-1, common punctuation, arrows and the four status shapes. All `.woff2` files together stay within 250 KB (`scripts/check-forbidden.sh`).
