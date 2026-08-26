// ── State ──
let currentMode = 'image'; // image | pdf | sketch
let currentInput = 'upload'; // upload | url
let selectedFile = null;
let selectedFormat = null;
let isConverting = false;
let sketchFile = null;

// ── DOM refs ──
const els = {};

function $(id) { return document.getElementById(id); }

document.addEventListener('DOMContentLoaded', () => {
  // Cache elements
  els.imageMode = $('image-mode');
  els.pdfMode = $('pdf-mode');
  els.sketchMode = $('sketch-mode');
  els.inputUpload = $('input-upload');
  els.inputUrl = $('input-url');
  els.dropZone = $('dropZone');
  els.fileInput = $('fileInput');
  els.urlInput = $('urlInput');
  els.fetchBtn = $('fetchBtn');
  els.inputFormat = $('inputFormat');
  els.outputFormat = $('outputFormat');
  els.convertBtn = $('convertBtn');
  els.pdfDropZone = $('pdfDropZone');
  els.pdfFileInput = $('pdfFileInput');
  els.pdfOutputFormat = $('pdfOutputFormat');
  els.pdfConvertBtn = $('pdfConvertBtn');

  // Sketch mode
  els.sketchDropZone = $('sketchDropZone');
  els.sketchFileInput = $('sketchFileInput');
  els.sketchConvertBtn = $('sketchConvertBtn');
  els.sigmaSlider = $('sigmaSlider');
  els.sigmaVal = $('sigmaVal');

  els.status = $('status');
  els.statusText = $('statusText');
  els.result = $('result');
  els.resultIcon = $('resultIcon');
  els.resultText = $('resultText');
  els.downloadLink = $('downloadLink');
  els.resetBtn = $('resetBtn');

  // ── Register events ──

  // Mode tabs
  document.querySelectorAll('.mode-tabs .tab-btn').forEach(btn => {
    btn.addEventListener('click', () => switchMode(btn.dataset.mode));
  });

  // Input tabs
  document.querySelectorAll('.input-tabs .tab-btn').forEach(btn => {
    btn.addEventListener('click', () => switchInput(btn.dataset.input));
  });

  // Drag & drop
  setupDropZone(els.dropZone, els.fileInput, handleFileSelect);
  setupDropZone(els.pdfDropZone, els.pdfFileInput, handlePdfSelect);
  setupDropZone(els.sketchDropZone, els.sketchFileInput, handleSketchSelect);

  // File input change
  els.fileInput.addEventListener('change', e => {
    if (e.target.files.length > 0) handleFileSelect(e.target.files[0]);
  });

  els.pdfFileInput.addEventListener('change', e => {
    if (e.target.files.length > 0) handlePdfSelect(e.target.files[0]);
  });

  els.sketchFileInput.addEventListener('change', e => {
    if (e.target.files.length > 0) handleSketchSelect(e.target.files[0]);
  });

  // URL input
  els.urlInput.addEventListener('input', updateConvertBtn);
  els.urlInput.addEventListener('keydown', e => {
    if (e.key === 'Enter') startConvert();
  });

  // Fetch button for URL
  els.fetchBtn.addEventListener('click', startConvert);

  // Format change
  els.outputFormat.addEventListener('change', updateConvertBtn);
  els.pdfOutputFormat.addEventListener('change', updateConvertBtn);

  // Convert buttons
  els.convertBtn.addEventListener('click', startConvert);
  els.pdfConvertBtn.addEventListener('click', startPdfConvert);
  els.sketchConvertBtn.addEventListener('click', startSketchConvert);

  // Slider value sync
  setupSliderSync('colorPrecision', 'colorPrecisionVal');
  setupSliderSync('filterSpeckle', 'filterSpeckleVal');
  setupSliderSync('cornerThreshold', 'cornerThresholdVal');
  setupSliderSync('sigmaSlider', 'sigmaVal');

  // Reset
  els.resetBtn.addEventListener('click', resetAll);

  // Load formats on startup
  loadFormats();
});

// ── Mode switching ──

function switchMode(mode) {
  currentMode = mode;
  document.querySelectorAll('.mode-tabs .tab-btn').forEach(b => b.classList.toggle('active', b.dataset.mode === mode));
  els.imageMode.classList.toggle('active', mode === 'image');
  els.pdfMode.classList.toggle('active', mode === 'pdf');
  els.sketchMode.classList.toggle('active', mode === 'sketch');
  hideResult();
}

function switchInput(input) {
  currentInput = input;
  document.querySelectorAll('.input-tabs .tab-btn').forEach(b => b.classList.toggle('active', b.dataset.input === input));
  els.inputUpload.classList.toggle('active', input === 'upload');
  els.inputUrl.classList.toggle('active', input === 'url');
  updateConvertBtn();
}

// ── Drag & Drop ──

function setupDropZone(zone, fileInput, callback) {
  zone.addEventListener('click', () => fileInput.click());

  zone.addEventListener('dragover', e => {
    e.preventDefault();
    zone.classList.add('drag-over');
  });

  zone.addEventListener('dragleave', () => {
    zone.classList.remove('drag-over');
  });

  zone.addEventListener('drop', e => {
    e.preventDefault();
    zone.classList.remove('drag-over');
    const files = e.dataTransfer.files;
    if (files.length > 0) callback(files[0]);
  });
}

function handleFileSelect(file) {
  selectedFile = file;
  // Update drop zone display
  els.dropZone.querySelector('.drop-content').innerHTML = `
    <svg width="36" height="36" viewBox="0 0 24 24" fill="none" stroke="#22c55e" stroke-width="1.5">
      <path d="M14 2H6a2 2 0 00-2 2v16a2 2 0 002 2h12a2 2 0 002-2V8z"/>
      <polyline points="14 2 14 8 20 8"/>
    </svg>
    <p style="color: #22c55e;">${file.name}</p>
    <p class="hint">${(file.size / 1024 / 1024).toFixed(2)} MB</p>
  `;
  // Auto-detect format
  const ext = file.name.split('.').pop().toLowerCase();
  if (['jpg','jpeg','png','bmp','tiff','tif','webp','gif'].includes(ext)) {
    els.inputFormat.value = ext === 'jpeg' ? 'jpg' : ext === 'tif' ? 'tiff' : ext;
  }
  updateConvertBtn();
}

function handlePdfSelect(file) {
  selectedFile = file;
  els.pdfDropZone.querySelector('.drop-content').innerHTML = `
    <svg width="36" height="36" viewBox="0 0 24 24" fill="none" stroke="#22c55e" stroke-width="1.5">
      <path d="M14 2H6a2 2 0 00-2 2v16a2 2 0 002 2h12a2 2 0 002-2V8z"/>
      <polyline points="14 2 14 8 20 8"/>
    </svg>
    <p style="color: #22c55e;">${file.name}</p>
    <p class="hint">${(file.size / 1024 / 1024).toFixed(2)} MB</p>
  `;
  els.pdfOutputFormat.disabled = false;
  els.pdfConvertBtn.disabled = false;
}

function handleSketchSelect(file) {
  sketchFile = file;
  els.sketchDropZone.querySelector('.drop-content').innerHTML = `
    <svg width="36" height="36" viewBox="0 0 24 24" fill="none" stroke="#22c55e" stroke-width="1.5">
      <path d="M14 2H6a2 2 0 00-2 2v16a2 2 0 002 2h12a2 2 0 002-2V8z"/>
      <polyline points="14 2 14 8 20 8"/>
    </svg>
    <p style="color: #22c55e;">${file.name}</p>
    <p class="hint">${(file.size / 1024 / 1024).toFixed(2)} MB</p>
  `;
  els.sketchConvertBtn.disabled = false;
}

// ── Button state ──

function updateConvertBtn() {
  if (currentMode === 'image') {
    if (currentInput === 'upload') {
      els.convertBtn.disabled = !selectedFile;
      els.fetchBtn.disabled = true;
    } else {
      els.convertBtn.disabled = !els.urlInput.value.trim();
      els.fetchBtn.disabled = !els.urlInput.value.trim();
    }
  }
}

// ── Conversion ──

async function startConvert() {
  if (isConverting) return;
  if (currentInput === 'upload' && !selectedFile) return;
  if (currentInput === 'url') {
    const url = els.urlInput.value.trim();
    if (!url) return;
  }

  isConverting = true;
  showStatus('正在转换，请稍候...');

  try {
    if (currentInput === 'url') {
      await urlConvert(els.urlInput.value.trim());
    } else {
      await uploadConvert();
    }
  } catch (err) {
    showError(err.message || '转换失败，请重试');
  } finally {
    isConverting = false;
  }
}

async function uploadConvert() {
  const formData = new FormData();
  formData.append('file', selectedFile);
  formData.append('output', els.outputFormat.value);
  // 精度参数
  formData.append('mode', els.precisionMode?.value || 'spline');
  formData.append('color_precision', els.colorPrecision?.value || '6');
  formData.append('filter_speckle', els.filterSpeckle?.value || '4');
  formData.append('corner_threshold', els.cornerThreshold?.value || '60');

  const resp = await fetch('/api/convert/upload', {
    method: 'POST',
    body: formData,
  });

  const data = await resp.json();
  if (!data.success) throw new Error(data.error);
  showResult(data.download_url, data.format);
}

async function urlConvert(url) {
  const output = els.outputFormat.value;
  const resp = await fetch(`/api/convert/url?output=${encodeURIComponent(output)}&url=${encodeURIComponent(url)}`, {
    method: 'POST',
  });

  const data = await resp.json();
  if (!data.success) throw new Error(data.error);
  showResult(data.download_url, data.format);
}

async function startPdfConvert() {
  if (isConverting || !selectedFile) return;
  isConverting = true;
  showStatus('正在转换PDF，请稍候...');

  try {
    const formData = new FormData();
    formData.append('file', selectedFile);
    formData.append('output', els.pdfOutputFormat.value);

    const resp = await fetch('/api/convert/pdf-to-office', {
      method: 'POST',
      body: formData,
    });

    const data = await resp.json();
    if (!data.success) throw new Error(data.error);
    showResult(data.download_url, data.format);
  } catch (err) {
    showError(err.message || '转换失败，请重试');
  } finally {
    isConverting = false;
  }
}

// ── Sketch conversion ──

async function startSketchConvert() {
  if (isConverting || !sketchFile) return;
  isConverting = true;
  showStatus('正在生成素描，请稍候...');

  try {
    const formData = new FormData();
    formData.append('file', sketchFile);
    formData.append('sigma', els.sigmaSlider.value || '3.0');

    const resp = await fetch('/api/convert/sketch', {
      method: 'POST',
      body: formData,
    });

    const data = await resp.json();
    if (!data.success) throw new Error(data.error || '素描生成失败');
    showResult(data.download_url, 'png');
  } catch (err) {
    showError(err.message || '生成失败，请重试');
  } finally {
    isConverting = false;
  }
}

// ── Slider sync ──

function setupSliderSync(sliderId, labelId) {
  const slider = $(sliderId);
  const label = $(labelId);
  if (!slider || !label) return;
  slider.addEventListener('input', () => {
    label.textContent = slider.value;
  });
}

// ── UI helpers ──

function showStatus(text) {
  els.status.hidden = false;
  els.result.hidden = true;
  els.statusText.textContent = text;
}

function showResult(downloadUrl, format) {
  els.status.hidden = true;
  els.result.hidden = false;
  els.resultIcon.innerHTML = `
    <svg width="40" height="40" viewBox="0 0 24 24" fill="none" stroke="#22c55e" stroke-width="2">
      <path d="M22 11.08V12a10 10 0 11-5.93-9.14"/>
      <polyline points="22 4 12 14.01 9 11.01"/>
    </svg>`;
  els.resultText.textContent = `转换成功！文件格式: .${format}`;
  els.resultText.style.color = '';

  const url = downloadUrl + (downloadUrl.includes('?') ? '&' : '?') + '_dl=' + Date.now();

  const isWechat = /MicroMessenger|wxwork/i.test(navigator.userAgent);

  els.downloadLink.href = url;
  els.downloadLink.textContent = '📥 下载文件';
  els.downloadLink.style.display = 'inline-flex';

  if (isWechat) {
    els.downloadLink.target = '_blank';
  } else {
    els.downloadLink.target = '_self';
  }
}

function showError(msg) {
  els.status.hidden = true;
  els.result.hidden = false;
  els.resultIcon.innerHTML = `
    <svg width="40" height="40" viewBox="0 0 24 24" fill="none" stroke="#ef4444" stroke-width="2">
      <circle cx="12" cy="12" r="10"/>
      <line x1="15" y1="9" x2="9" y2="15"/>
      <line x1="9" y1="9" x2="15" y2="15"/>
    </svg>`;
  els.resultText.textContent = msg;
  els.resultText.style.color = '#ef4444';
  els.downloadLink.style.display = 'none';
}

function hideResult() {
  els.status.hidden = true;
  els.result.hidden = true;
}

function resetAll() {
  hideResult();
  selectedFile = null;
  selectedFormat = null;
  sketchFile = null;

  // Reset drop zones
  els.dropZone.querySelector('.drop-content').innerHTML = `
    <svg width="48" height="48" viewBox="0 0 24 24" fill="none" stroke="#6366f1" stroke-width="1.5">
      <path d="M21 15v4a2 2 0 01-2 2H5a2 2 0 01-2-2v-4"/>
      <polyline points="17 8 12 3 7 8"/>
      <line x1="12" y1="3" x2="12" y2="15"/>
    </svg>
    <p>拖拽文件到此处，或 <span class="link">点击选择</span></p>
    <p class="hint">支持 JPG / PNG / BMP / TIFF / WebP / GIF</p>
  `;

  els.pdfDropZone.querySelector('.drop-content').innerHTML = `
    <svg width="48" height="48" viewBox="0 0 24 24" fill="none" stroke="#6366f1" stroke-width="1.5">
      <path d="M14 2H6a2 2 0 00-2 2v16a2 2 0 002 2h12a2 2 0 002-2V8z"/>
      <polyline points="14 2 14 8 20 8"/>
      <line x1="16" y1="13" x2="8" y2="13"/>
      <line x1="16" y1="17" x2="8" y2="17"/>
    </svg>
    <p>拖拽PDF文件到此处，或 <span class="link">点击选择</span></p>
    <p class="hint">支持 PDF 格式</p>
  `;

  els.sketchDropZone.querySelector('.drop-content').innerHTML = `
    <svg width="48" height="48" viewBox="0 0 24 24" fill="none" stroke="#6366f1" stroke-width="1.5">
      <path d="M21 15v4a2 2 0 01-2 2H5a2 2 0 01-2-2v-4"/>
      <polyline points="17 8 12 3 7 8"/>
      <line x1="12" y1="3" x2="12" y2="15"/>
    </svg>
    <p>拖拽照片到此处，或 <span class="link">点击选择</span></p>
    <p class="hint">支持 JPG / PNG / BMP / WebP，自动转为铅笔素描风格</p>
  `;

  els.urlInput.value = '';
  els.inputFormat.value = 'auto';
  els.outputFormat.value = 'svg';
  els.pdfOutputFormat.value = 'docx';
  els.pdfOutputFormat.disabled = true;
  els.convertBtn.disabled = true;
  els.pdfConvertBtn.disabled = true;
  els.sketchConvertBtn.disabled = true;
  els.downloadLink.style.display = 'inline-flex';
  els.resultText.style.color = '';
}

// ── Load format info ──

async function loadFormats() {
  try {
    const resp = await fetch('/api/formats');
    // Just verify the API exists
  } catch (e) {
    // API might not be available yet during dev
  }
}