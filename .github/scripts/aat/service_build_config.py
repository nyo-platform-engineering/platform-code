"""The seven deployed AAT images and the source paths that affect each build."""
from pathlib import Path

REPOSITORY_ROOT = Path(__file__).resolve().parents[3]
AAT_SOURCE_DIRECTORY = "code/apps/aat/"
SERVICE_SOURCE_FOLDERS = {
    "aggregator": "aggregator",
    "auth": "auth",
    "client": "client",
    "bmkg": "bmkg",
    "pvmbg": "pvmbg",
    "dashboard-updater": "consumers/dashboard-updater",
    "field-notifier": "consumers/field-notifier",
}


def services_affected_by_changes(paths):
    affected = set()
    for path in paths:
        # Documentation never changes the Go binary.
        if path.endswith(".md"):
            continue
        if (path == ".github/workflows/aat-ci.yml"
                or path.startswith(".github/scripts/aat/")
                or path in {AAT_SOURCE_DIRECTORY + "go.mod", AAT_SOURCE_DIRECTORY + "go.sum", AAT_SOURCE_DIRECTORY + ".dockerignore"}
                or path.startswith(AAT_SOURCE_DIRECTORY + "internal/")):
            affected.update(SERVICE_SOURCE_FOLDERS)
        elif path.startswith(AAT_SOURCE_DIRECTORY + "consumers/"):
            # Both consumer Dockerfiles COPY the entire consumers directory.
            affected.update(("dashboard-updater", "field-notifier"))
        else:
            for service, folder in SERVICE_SOURCE_FOLDERS.items():
                if path.startswith(AAT_SOURCE_DIRECTORY + folder + "/"):
                    affected.add(service)
    return sorted(affected)
