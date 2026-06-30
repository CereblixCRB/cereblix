/* ============================================================================
   qr.js — self-contained QR Code generator (byte mode, versions 1–40, all masks)
   ----------------------------------------------------------------------------
   Adapted from Project Nayuki's "QR Code generator library" (compact JS port).
   Copyright (c) Project Nayuki — MIT License.
   https://www.nayuki.io/page/qr-code-generator-library
   No network, no dependencies. Renders to an SVG string / element.
   ========================================================================== */
(function (global) {
  "use strict";

  // ---- Reed–Solomon / Galois field GF(256), modulus 0x11D ----
  function rsMultiply(x, y) {
    var z = 0;
    for (var i = 7; i >= 0; i--) {
      z = (z << 1) ^ ((z >>> 7) * 0x11D);
      z ^= ((y >>> i) & 1) * x;
    }
    return z & 0xFF;
  }
  function rsDivisor(degree) {
    var result = [];
    for (var i = 0; i < degree - 1; i++) result.push(0);
    result.push(1);
    var root = 1;
    for (i = 0; i < degree; i++) {
      for (var j = 0; j < result.length; j++) {
        result[j] = rsMultiply(result[j], root);
        if (j + 1 < result.length) result[j] ^= result[j + 1];
      }
      root = rsMultiply(root, 0x02);
    }
    return result;
  }
  function rsRemainder(data, divisor) {
    var result = divisor.map(function () { return 0; });
    for (var i = 0; i < data.length; i++) {
      var factor = data[i] ^ result.shift();
      result.push(0);
      for (var j = 0; j < divisor.length; j++) result[j] ^= rsMultiply(divisor[j], factor);
    }
    return result;
  }

  // ---- error-correction tables (index by [eclOrdinal][version]) ----
  // ecl ordinal: L=0, M=1, Q=2, H=3
  var ECC_CW_PER_BLOCK = [
    [-1, 7, 10, 15, 20, 26, 18, 20, 24, 30, 18, 20, 24, 26, 30, 22, 24, 28, 30, 28, 28, 28, 28, 30, 30, 26, 28, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30],
    [-1, 10, 16, 26, 18, 24, 16, 18, 22, 22, 26, 30, 22, 22, 24, 24, 28, 28, 26, 26, 26, 26, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28],
    [-1, 13, 22, 18, 26, 18, 24, 18, 22, 20, 24, 28, 26, 24, 20, 30, 24, 28, 28, 26, 30, 28, 30, 30, 30, 30, 28, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30],
    [-1, 17, 28, 22, 16, 22, 28, 26, 26, 24, 28, 24, 28, 22, 24, 24, 30, 28, 28, 26, 28, 30, 24, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30]
  ];
  var NUM_EC_BLOCKS = [
    [-1, 1, 1, 1, 1, 1, 2, 2, 2, 2, 4, 4, 4, 4, 4, 6, 6, 6, 6, 7, 8, 8, 9, 9, 10, 12, 12, 12, 13, 14, 15, 16, 17, 18, 19, 19, 20, 21, 22, 24, 25],
    [-1, 1, 1, 1, 2, 2, 4, 4, 4, 5, 5, 5, 8, 9, 9, 10, 10, 11, 13, 14, 16, 17, 17, 18, 20, 21, 23, 25, 26, 28, 29, 31, 33, 35, 37, 38, 40, 43, 45, 47, 49],
    [-1, 1, 1, 2, 2, 4, 4, 6, 6, 8, 8, 8, 10, 12, 16, 12, 17, 16, 18, 21, 20, 23, 23, 25, 27, 29, 34, 34, 35, 38, 40, 43, 45, 48, 51, 53, 56, 59, 62, 65, 68],
    [-1, 1, 1, 2, 4, 4, 4, 5, 6, 8, 8, 11, 11, 16, 16, 18, 16, 19, 21, 25, 25, 25, 34, 30, 32, 35, 37, 40, 42, 45, 48, 51, 54, 57, 60, 63, 66, 70, 74, 77, 81]
  ];
  var ECL_FORMAT_BITS = { L: 1, M: 0, Q: 3, H: 2 };
  var ECL_ORDINAL = { L: 0, M: 1, Q: 2, H: 3 };

  var PEN_N1 = 3, PEN_N2 = 3, PEN_N3 = 40, PEN_N4 = 10;

  function getBit(x, i) { return ((x >>> i) & 1) !== 0; }

  function utf8Bytes(str) {
    if (typeof TextEncoder !== "undefined") return Array.from(new TextEncoder().encode(str));
    var enc = unescape(encodeURIComponent(str)), out = [];
    for (var i = 0; i < enc.length; i++) out.push(enc.charCodeAt(i));
    return out;
  }

  function numRawDataModules(ver) {
    var result = (16 * ver + 128) * ver + 64;
    if (ver >= 2) {
      var numAlign = Math.floor(ver / 7) + 2;
      result -= (25 * numAlign - 10) * numAlign - 55;
      if (ver >= 7) result -= 36;
    }
    return result;
  }
  function numDataCodewords(ver, eclOrd) {
    return Math.floor(numRawDataModules(ver) / 8) -
      ECC_CW_PER_BLOCK[eclOrd][ver] * NUM_EC_BLOCKS[eclOrd][ver];
  }

  function QrCode(version, eclOrd, dataCodewords) {
    this.version = version;
    this.size = version * 4 + 17;
    var size = this.size;
    this.modules = []; this.isFunction = [];
    for (var i = 0; i < size; i++) {
      this.modules.push(new Array(size).fill(false));
      this.isFunction.push(new Array(size).fill(false));
    }
    this.drawFunctionPatterns();
    var allCodewords = this.addEccAndInterleave(dataCodewords, version, eclOrd);
    this.drawCodewords(allCodewords);

    // choose best mask
    var minPenalty = Infinity, bestMask = 0;
    for (var m = 0; m < 8; m++) {
      this.applyMask(m);
      this.drawFormatBits(eclOrd, m);
      var p = this.getPenaltyScore();
      if (p < minPenalty) { minPenalty = p; bestMask = m; }
      this.applyMask(m); // undo
    }
    this.applyMask(bestMask);
    this.drawFormatBits(eclOrd, bestMask);
  }

  QrCode.prototype.setFn = function (x, y, isDark) {
    this.modules[y][x] = isDark;
    this.isFunction[y][x] = true;
  };

  QrCode.prototype.drawFunctionPatterns = function () {
    var size = this.size, i;
    for (i = 0; i < size; i++) {
      this.setFn(6, i, i % 2 === 0);
      this.setFn(i, 6, i % 2 === 0);
    }
    this.drawFinder(3, 3);
    this.drawFinder(size - 4, 3);
    this.drawFinder(3, size - 4);

    var pos = this.alignPositions(), n = pos.length;
    for (i = 0; i < n; i++) {
      for (var j = 0; j < n; j++) {
        if (!((i === 0 && j === 0) || (i === 0 && j === n - 1) || (i === n - 1 && j === 0)))
          this.drawAlign(pos[i], pos[j]);
      }
    }
    this.drawFormatBits(0, 0); // placeholder; real bits drawn after masking
    this.drawVersionInfo();
  };

  QrCode.prototype.drawFinder = function (x, y) {
    for (var dy = -4; dy <= 4; dy++) {
      for (var dx = -4; dx <= 4; dx++) {
        var dist = Math.max(Math.abs(dx), Math.abs(dy));
        var xx = x + dx, yy = y + dy;
        if (xx >= 0 && xx < this.size && yy >= 0 && yy < this.size)
          this.setFn(xx, yy, dist !== 2 && dist !== 4);
      }
    }
  };
  QrCode.prototype.drawAlign = function (x, y) {
    for (var dy = -2; dy <= 2; dy++)
      for (var dx = -2; dx <= 2; dx++)
        this.setFn(x + dx, y + dy, Math.max(Math.abs(dx), Math.abs(dy)) !== 1);
  };

  QrCode.prototype.alignPositions = function () {
    var ver = this.version;
    if (ver === 1) return [];
    var numAlign = Math.floor(ver / 7) + 2;
    var step = (ver === 32) ? 26 : Math.ceil((ver * 4 + 4) / (numAlign * 2 - 2)) * 2;
    var result = [6];
    for (var p = this.size - 7; result.length < numAlign; p -= step) result.splice(1, 0, p);
    return result;
  };

  QrCode.prototype.drawFormatBits = function (eclOrd, mask) {
    var fb = ECL_FORMAT_BITS[["L", "M", "Q", "H"][eclOrd]];
    var data = (fb << 3) | mask, rem = data, i;
    for (i = 0; i < 10; i++) rem = (rem << 1) ^ ((rem >>> 9) * 0x537);
    var bits = ((data << 10) | rem) ^ 0x5412;
    var size = this.size;
    for (i = 0; i <= 5; i++) this.setFn(8, i, getBit(bits, i));
    this.setFn(8, 7, getBit(bits, 6));
    this.setFn(8, 8, getBit(bits, 7));
    this.setFn(7, 8, getBit(bits, 8));
    for (i = 9; i < 15; i++) this.setFn(14 - i, 8, getBit(bits, i));
    for (i = 0; i < 8; i++) this.setFn(size - 1 - i, 8, getBit(bits, i));
    for (i = 8; i < 15; i++) this.setFn(8, size - 15 + i, getBit(bits, i));
    this.setFn(8, size - 8, true);
  };

  QrCode.prototype.drawVersionInfo = function () {
    if (this.version < 7) return;
    var rem = this.version, i;
    for (i = 0; i < 12; i++) rem = (rem << 1) ^ ((rem >>> 11) * 0x1F25);
    var bits = (this.version << 12) | rem;
    for (i = 0; i < 18; i++) {
      var color = getBit(bits, i);
      var a = this.size - 11 + (i % 3), b = Math.floor(i / 3);
      this.setFn(a, b, color);
      this.setFn(b, a, color);
    }
  };

  QrCode.prototype.addEccAndInterleave = function (data, ver, eclOrd) {
    var numBlocks = NUM_EC_BLOCKS[eclOrd][ver];
    var blockEccLen = ECC_CW_PER_BLOCK[eclOrd][ver];
    var rawCodewords = Math.floor(numRawDataModules(ver) / 8);
    var numShort = numBlocks - (rawCodewords % numBlocks);
    var shortLen = Math.floor(rawCodewords / numBlocks);
    var blocks = [], rsDiv = rsDivisor(blockEccLen);
    for (var i = 0, k = 0; i < numBlocks; i++) {
      var dat = data.slice(k, k + shortLen - blockEccLen + (i < numShort ? 0 : 1));
      k += dat.length;
      var ecc = rsRemainder(dat, rsDiv);
      if (i < numShort) dat.push(0);
      blocks.push(dat.concat(ecc));
    }
    var result = [];
    for (i = 0; i < blocks[0].length; i++) {
      for (var j = 0; j < blocks.length; j++) {
        if (i !== shortLen - blockEccLen || j >= numShort) result.push(blocks[j][i]);
      }
    }
    return result;
  };

  QrCode.prototype.drawCodewords = function (data) {
    var size = this.size, i = 0;
    for (var right = size - 1; right >= 1; right -= 2) {
      if (right === 6) right = 5;
      for (var vert = 0; vert < size; vert++) {
        for (var j = 0; j < 2; j++) {
          var x = right - j;
          var upward = ((right + 1) & 2) === 0;
          var y = upward ? size - 1 - vert : vert;
          if (!this.isFunction[y][x] && i < data.length * 8) {
            this.modules[y][x] = getBit(data[i >>> 3], 7 - (i & 7));
            i++;
          }
        }
      }
    }
  };

  QrCode.prototype.applyMask = function (mask) {
    var size = this.size;
    for (var y = 0; y < size; y++) {
      for (var x = 0; x < size; x++) {
        var invert;
        switch (mask) {
          case 0: invert = (x + y) % 2 === 0; break;
          case 1: invert = y % 2 === 0; break;
          case 2: invert = x % 3 === 0; break;
          case 3: invert = (x + y) % 3 === 0; break;
          case 4: invert = (Math.floor(x / 3) + Math.floor(y / 2)) % 2 === 0; break;
          case 5: invert = (x * y) % 2 + (x * y) % 3 === 0; break;
          case 6: invert = ((x * y) % 2 + (x * y) % 3) % 2 === 0; break;
          case 7: invert = ((x + y) % 2 + (x * y) % 3) % 2 === 0; break;
        }
        if (!this.isFunction[y][x] && invert) this.modules[y][x] = !this.modules[y][x];
      }
    }
  };

  QrCode.prototype.getPenaltyScore = function () {
    var size = this.size, result = 0, x, y;
    // rule 1 + finder-like, rows
    for (y = 0; y < size; y++) {
      var runColor = false, runX = 0, hist = [0, 0, 0, 0, 0, 0, 0];
      for (x = 0; x < size; x++) {
        if (this.modules[y][x] === runColor) {
          runX++;
          if (runX === 5) result += PEN_N1; else if (runX > 5) result++;
        } else {
          this.finderAddHistory(runX, hist, size);
          if (!runColor) result += this.finderCount(hist) * PEN_N3;
          runColor = this.modules[y][x]; runX = 1;
        }
      }
      result += this.finderTerminate(runColor, runX, hist, size) * PEN_N3;
    }
    // rule 1 + finder-like, columns
    for (x = 0; x < size; x++) {
      var rc = false, rY = 0, h2 = [0, 0, 0, 0, 0, 0, 0];
      for (y = 0; y < size; y++) {
        if (this.modules[y][x] === rc) {
          rY++;
          if (rY === 5) result += PEN_N1; else if (rY > 5) result++;
        } else {
          this.finderAddHistory(rY, h2, size);
          if (!rc) result += this.finderCount(h2) * PEN_N3;
          rc = this.modules[y][x]; rY = 1;
        }
      }
      result += this.finderTerminate(rc, rY, h2, size) * PEN_N3;
    }
    // rule 2: 2x2 blocks
    for (y = 0; y < size - 1; y++) {
      for (x = 0; x < size - 1; x++) {
        var c = this.modules[y][x];
        if (c === this.modules[y][x + 1] && c === this.modules[y + 1][x] && c === this.modules[y + 1][x + 1])
          result += PEN_N2;
      }
    }
    // rule 4: dark balance
    var dark = 0;
    for (y = 0; y < size; y++) for (x = 0; x < size; x++) if (this.modules[y][x]) dark++;
    var total = size * size;
    var k = Math.ceil(Math.abs(dark * 20 - total * 10) / total) - 1;
    result += k * PEN_N4;
    return result;
  };

  QrCode.prototype.finderAddHistory = function (run, hist, size) {
    if (hist[0] === 0) run += size; // light border on initial run
    hist.pop(); hist.unshift(run);
  };
  QrCode.prototype.finderTerminate = function (color, run, hist, size) {
    if (color) { this.finderAddHistory(run, hist, size); run = 0; }
    run += size;
    this.finderAddHistory(run, hist, size);
    return this.finderCount(hist);
  };
  QrCode.prototype.finderCount = function (h) {
    var n = h[1];
    var core = n > 0 && h[2] === n && h[3] === n * 3 && h[4] === n && h[5] === n;
    return (core && h[0] >= n * 4 && h[6] >= n ? 1 : 0) +
           (core && h[6] >= n * 4 && h[0] >= n ? 1 : 0);
  };

  // ---- top-level encode ----
  function encode(text, ecl) {
    ecl = ecl || "M";
    var eclOrd = ECL_ORDINAL[ecl];
    var bytes = utf8Bytes(text);

    // pick smallest version that fits
    var version = -1, dataCapacityBits = 0, ccBits = 0;
    for (var v = 1; v <= 40; v++) {
      ccBits = (v <= 9) ? 8 : 16;
      var usedBits = 4 + ccBits + 8 * bytes.length;
      var cap = numDataCodewords(v, eclOrd) * 8;
      if (usedBits <= cap) { version = v; dataCapacityBits = cap; break; }
    }
    if (version === -1) throw new Error("data too long for QR");

    // build bit stream
    var bb = [];
    function appendBits(val, len) { for (var i = len - 1; i >= 0; i--) bb.push((val >>> i) & 1); }
    appendBits(0x4, 4);                 // byte mode
    appendBits(bytes.length, ccBits);   // char count
    for (var i = 0; i < bytes.length; i++) appendBits(bytes[i], 8);
    appendBits(0, Math.min(4, dataCapacityBits - bb.length)); // terminator
    while (bb.length % 8 !== 0) bb.push(0);
    for (var pad = 0xEC; bb.length < dataCapacityBits; pad ^= 0xEC ^ 0x11) appendBits(pad, 8);

    var dataCodewords = [];
    for (i = 0; i < bb.length; i += 8) {
      var bvte = 0;
      for (var b = 0; b < 8; b++) bvte = (bvte << 1) | bb[i + b];
      dataCodewords.push(bvte);
    }
    return new QrCode(version, eclOrd, dataCodewords);
  }

  // ---- render to SVG string ----
  function toSvg(text, opts) {
    opts = opts || {};
    var border = opts.border == null ? 4 : opts.border;
    var dark = opts.dark || "#0b0f16";
    var light = opts.light || "#ffffff";
    var qr = encode(text, opts.ecl || "M");
    var size = qr.size, dim = size + border * 2;
    var parts = [];
    for (var y = 0; y < size; y++) {
      for (var x = 0; x < size; x++) {
        if (qr.modules[y][x]) parts.push("M" + (x + border) + "," + (y + border) + "h1v1h-1z");
      }
    }
    return '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 ' + dim + ' ' + dim +
      '" shape-rendering="crispEdges" role="img" aria-label="QR code">' +
      '<rect width="' + dim + '" height="' + dim + '" fill="' + light + '"/>' +
      '<path d="' + parts.join("") + '" fill="' + dark + '"/></svg>';
  }

  function toElement(text, opts) {
    var wrap = document.createElement("div");
    wrap.className = "qr-card";
    wrap.innerHTML = toSvg(text, opts);
    return wrap;
  }

  global.QR = { encode: encode, toSvg: toSvg, toElement: toElement };
})(window);
