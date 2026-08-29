"""T-03-1..T-03-5: the pinned pipeline options -- RapidOCR/en/onnxruntime, cpu/2 threads,
table structure ACCURATE, code/formula/picture off.

Needs docling importable -- run only via the Docker `test` stage (see sidecar/docling/Dockerfile),
never in the local venv.
"""

from importlib import metadata

from docling.datamodel.accelerator_options import AcceleratorDevice
from docling.datamodel.pipeline_options import RapidOcrOptions, TableFormerMode

import convert


def test_t03_1_ocr_options_is_rapidocr_onnxruntime():
    opts = convert.pipeline_options()
    assert isinstance(opts.ocr_options, RapidOcrOptions)
    assert opts.ocr_options.kind == "rapidocr"
    assert opts.ocr_options.backend == "onnxruntime"


def test_t03_2_lang_is_exactly_en_not_the_chinese_default():
    # RapidOcrOptions().lang defaults to ["chinese"] -- confirmed against docling==2.123.1.
    # docling's own _DOCLING_LANG_NORMALIZE maps "english"->"en" before RapidOCR resolves it,
    # so ["en"] and ["english"] are equivalent; ["en"] is the AC's literal value and needs no
    # alias step, so that's what's asserted here.
    opts = convert.pipeline_options()
    assert opts.ocr_options.lang == ["en"]


def test_t03_3_easyocr_absent_onnxruntime_present():
    # Control needle (onnxruntime) + population floor: a scan that found nothing installed
    # would read "clean" by accident, not by proof.
    names = {d.metadata["Name"].lower() for d in metadata.distributions() if d.metadata.get("Name")}
    assert len(names) > 50, f"only {len(names)} distribution(s) scanned -- too few to mean anything"
    assert "onnxruntime" in names, "control needle missing: onnxruntime must be installed"
    assert "easyocr" not in names, (
        "bare docling ships rapidocr without onnxruntime; easyocr must never appear"
    )


def test_t03_4_accelerator_is_cpu_with_pinned_thread_count():
    opts = convert.pipeline_options()
    assert opts.accelerator_options.device == AcceleratorDevice.CPU
    assert opts.accelerator_options.num_threads == convert.DOCLING_NUM_THREADS
    assert opts.accelerator_options.num_threads != 4, (
        "4 is the auto-detected default this AC forbids"
    )


def test_t03_5_table_structure_is_explicit_and_accurate():
    opts = convert.pipeline_options()
    assert opts.do_table_structure is True
    assert opts.table_structure_options.mode == TableFormerMode.ACCURATE


def test_code_formula_picture_enrichment_stay_off():
    # Not individually named as a T-03-N id, but the same AC bullet as T-03-5's neighbours --
    # these are the axes that make baking the model ~560 MB and not ~1.2 GB.
    opts = convert.pipeline_options()
    assert opts.do_code_enrichment is False
    assert opts.do_formula_enrichment is False
    assert opts.do_picture_classification is False
