"""Vendor the app's Lucide SVG sources; no build, tests or previews are run."""

from concurrent.futures import ThreadPoolExecutor
from pathlib import Path
from urllib.error import HTTPError
from urllib.request import Request, urlopen
import xml.etree.ElementTree as ET


REVISION = "03f11e2d6adc319ea21e9a1d5820a99cd3767257"
BASE_URL = f"https://raw.githubusercontent.com/lucide-icons/lucide/{REVISION}"
DEST = Path(__file__).resolve().parents[1] / "style" / "lucide"
ICONS = """
arrow-down arrow-down-up arrow-up bell calendar check check-check chevron-down chevron-right circle-off
circle-alert circle-check circle-dot circle-pause clipboard clock copy disc-3 download
ellipsis external-link file file-archive film folder folder-open gauge globe grip-vertical hard-drive headphones house
info layers link list minus moon package panel-bottom panel-left
panel-right pause play plus rotate-cw rss search settings share-2
sliders-horizontal square sun trash upload x zap list-filter layout-list columns-3 list-checks funnel orbit
""".split()


def fetch(path):
    request = Request(f"{BASE_URL}/{path}", headers={"User-Agent": "opendl-icon-vendor"})
    try:
        with urlopen(request, timeout=30) as response:
            data = response.read()
    except HTTPError as error:
        raise RuntimeError(f"Cannot fetch Lucide source {path}: HTTP {error.code}") from error
    if path.endswith(".svg"):
        root = ET.fromstring(data)
        if root.tag != "{http://www.w3.org/2000/svg}svg" or root.get("viewBox") != "0 0 24 24":
            raise ValueError(f"Unexpected icon format: {path}")
    return path, data


def main():
    # Fetch everything before writing, so a failed fetch leaves sources intact.
    paths = ["LICENSE", *(f"icons/{name}.svg" for name in sorted(ICONS))]
    with ThreadPoolExecutor(max_workers=6) as pool:
        files = list(pool.map(fetch, paths))
    for name, data in files:
        target = DEST / name
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_bytes(data)
    print(f"Vendored {len(ICONS)} Lucide icons at {REVISION}")


if __name__ == "__main__":
    main()
