import importlib.metadata as metadata
import importlib.util as util
import json
import os
import re
import site
import sys
import sysconfig


def norm(value):
    return re.sub(r"[-_.]+", "-", value).lower()


def within(path, root):
    if not path or not root:
        return False
    try:
        root = os.path.realpath(root)
        return os.path.commonpath((os.path.realpath(path), root)) == root
    except (OSError, ValueError):
        return False


user_sites = site.getusersitepackages()
if isinstance(user_sites, str):
    user_sites = [user_sites]

system_sites = list(site.getsitepackages()) if hasattr(site, "getsitepackages") else []
system_sites.extend(
    value
    for value in (
        sysconfig.get_paths().get("purelib"),
        sysconfig.get_paths().get("platlib"),
        sys.prefix,
    )
    if value
)

result = {}


def record(name, path):
    path = os.path.realpath(path)
    try:
        scope_path = os.path.realpath(str(metadata.distribution(name).locate_file("")))
    except Exception:
        scope_path = path
    if any(within(scope_path, root) for root in user_sites):
        scope = "user"
    elif any(within(scope_path, root) for root in system_sites):
        scope = "system"
    else:
        scope = "unknown"
    result[name] = {"path": path, "scope": scope}


distributions = {}
for module, names in metadata.packages_distributions().items():
    for name in names:
        distributions.setdefault(norm(name), []).append(module)

for name in sys.argv[1:]:
    try:
        modules = sorted(
            set(distributions.get(norm(name), ())),
            key=lambda value: (
                norm(value) != norm(name),
                value.startswith("_"),
                value,
            ),
        )
        for module in modules:
            try:
                spec = util.find_spec(module)
            except (ImportError, AttributeError, ValueError):
                continue
            if spec is None:
                continue
            locations = list(spec.submodule_search_locations or ())
            path = locations[0] if locations else spec.origin
            if path and os.path.exists(path):
                record(name, path)
                break
        if name in result:
            continue

        dist = metadata.distribution(name)
        declared = {
            norm(value)
            for value in (dist.read_text("top_level.txt") or "").splitlines()
            if value.strip()
        }
        candidates = []
        seen = set()
        for entry in dist.files or ():
            if not entry.parts:
                continue
            root = entry.parts[0]
            lower = root.lower()
            if root.startswith("..") or lower.endswith(
                (".dist-info", ".egg-info", ".data")
            ):
                continue
            path = os.path.realpath(str(dist.locate_file(root)))
            is_module = os.path.isdir(path) or (
                os.path.isfile(path)
                and lower.endswith((".py", ".pyi", ".so", ".dylib"))
            )
            if not is_module or path in seen:
                continue
            seen.add(path)
            root_name = norm(os.path.splitext(root)[0])
            score = 0 if root_name in declared else (1 if root_name == norm(name) else 2)
            candidates.append((score, root, path))
        if candidates:
            record(name, min(candidates)[2])
    except Exception:
        pass

print(json.dumps(result))
