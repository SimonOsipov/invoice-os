"""Fetch Docling's layout and TableFormer models at pinned commits, hash-verified.

Replaces docling's download_models(), which takes no revision and so follows moving refs.
Commits: heron repos = main as of 2026-11-15; docling-models = tag v2.3.0. Layout/TableFormer
only -- no code/formula (~640 MB) or picture classifier. No retry, no fallback to another ref:
a failed download or a hash mismatch fails the build.
"""

import hashlib
from pathlib import Path

# Same root and `--`-named subdirectories as download_models / TableStructureModel.
_ROOT = Path("/opt/docling-models")

_GITATTR = "11ad7efa24975ee4b0c3c3a38ed18737f0658a5f75a0a96787b576a78a023361"

# (repo_id, commit, {repo-relative path: sha256})
_REPOS: list[tuple[str, str, dict[str, str]]] = [
    (
        "docling-project/docling-layout-heron",
        "8f39ad3c0b4c58e9c2d2c84a38465abf757272d8",
        {
            ".gitattributes": _GITATTR,
            "README.md": "175700839bc7808eac6af1d0c23e4f483606ab2276fe01122f4093e61a1a65b6",
            "config.json": "fdea30805ce2f5666b147fca941dcdd27ad468e27d6ed21902207d3da056a97d",
            "docling_heron_400.png": "e7f78610372b32a7938e480d2c7fa1c3037ee170bd82282a5bd026232f6e6f9e",
            "model.safetensors": "00333a43451945aaf89db8ca9c0a17e75d1537c17db60fdb91aa95f4c7929e0c",
            "preprocessor_config.json": "cd38cd59999e7a95d68e487fbe5132df3d4e5c32a0836add57e6126ba0c4eaf1",
        },
    ),
    (
        "docling-project/docling-layout-heron-onnx",
        "40bde044036bb181c130ddf6c51792187268748f",
        {
            ".gitattributes": _GITATTR,
            "README.md": "48de0f07390f6823a225ce8aab348e7b57408534b56b71f1433ed6280456597f",
            "config.json": "c6e67b6cdf64fa245779d0409bb994aa96e8c1790e15ec07a65843628e232180",
            "model.onnx": "59c81a3a2923042d85034ffc487f8f47e4854117e879aef89b2b9f728fb4922a",
            "preprocessor_config.json": "54d086cf0d7d371f7fac36e5d3a0dae31e211298affc816106cc376506799d58",
        },
    ),
    (
        "docling-project/docling-models",
        "fc0f2d45e2218ea24bce5045f58a389aed16dc23",
        {
            ".gitattributes": "360aba3ff7ce75396f13f0a77925fe9e79997e8d4bac064595ebb00c82700386",
            ".gitignore": "b44b404189eaff3631c470946e7350c2e88cead304d332a8ad50325e6f56e95d",
            "README.md": "d17f233378eff1240b623b36da76ee8b40afcca05d505949713bf03f7e00822a",
            "config.json": "9c34024dc28ff47b75818f415e769809798c29bf9bde6f2ccc63a4acb62396d9",
            "model_artifacts/tableformer/accurate/tableformer_accurate.safetensors": "2a7d6c924b3cd12fb99a09280ca9c33a89c5d60b93253617d2e088c1a40374d9",
            "model_artifacts/tableformer/accurate/tm_config.json": "984e122ceb8ccf84d84c9d2882f6f2302a44b4f1e577babd6289892c36f3cffd",
            "model_artifacts/tableformer/fast/tableformer_fast.safetensors": "3119563aab5a7c96fda4d621119b63fd8806272b86c30936d15507616422f718",
            "model_artifacts/tableformer/fast/tm_config.json": "dca6762508dddfae6d57d6cb4ef822c6000119dff0f3b6489db7413118c2622a",
        },
    ),
]


def verify(local_dir: Path, want: dict[str, str]) -> None:
    """Fail unless local_dir holds exactly the files in want, with matching SHA-256.
    Skips .cache/, huggingface_hub's own metadata."""
    found = {
        p.relative_to(local_dir).as_posix(): p
        for p in local_dir.rglob("*")
        if p.is_file() and ".cache" not in p.relative_to(local_dir).parts[:1]
    }
    for rel in sorted(want.keys() - found.keys()):
        raise SystemExit(f"{local_dir / rel}: missing")
    for rel in sorted(found.keys() - want.keys()):
        raise SystemExit(f"{found[rel]}: not in the recorded manifest")
    for rel, expected in sorted(want.items()):
        got = hashlib.sha256(found[rel].read_bytes()).hexdigest()
        if got != expected:
            raise SystemExit(f"{found[rel]}: sha256 {got} != expected {expected}")


def main() -> None:
    from huggingface_hub import snapshot_download

    for repo_id, commit, want in _REPOS:
        local_dir = _ROOT / repo_id.replace("/", "--")
        snapshot_download(repo_id=repo_id, revision=commit, local_dir=local_dir)
        verify(local_dir, want)
        print(f"[fetch_docling_models] {repo_id}@{commit[:12]}: {len(want)} files sha256 OK")


if __name__ == "__main__":
    main()
