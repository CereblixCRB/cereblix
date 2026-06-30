package com.cereblix.wallet

import androidx.compose.foundation.Canvas
import androidx.compose.runtime.Composable
import androidx.compose.runtime.remember
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.geometry.Size
import androidx.compose.ui.graphics.Color
import kotlin.math.abs
import kotlin.math.max
import kotlin.math.min

/*
 * Tiny, self-contained QR Code generator (UTF-8 byte mode, versions 1..40, auto
 * mask). Pure Kotlin, NO dependencies (so we avoid pulling in ZXing).
 *
 * Ported and trimmed from the MIT-licensed Project Nayuki "QR Code generator
 * library" (https://www.nayuki.io/page/qr-code-generator-library).
 *
 * Copyright (c) Project Nayuki. (MIT License)
 * Permission is hereby granted, free of charge, to any person obtaining a copy of
 * this software and associated documentation files (the "Software"), to deal in
 * the Software without restriction, including without limitation the rights to
 * use, copy, modify, merge, publish, distribute, sublicense, and/or sell copies
 * of the Software, and to permit persons to whom the Software is furnished to do
 * so, subject to the above copyright notice and this permission notice being
 * included in all copies or substantial portions of the Software.
 */

enum class Ecc(val formatBits: Int) { LOW(1), MEDIUM(0), QUARTILE(3), HIGH(2) }

class QrCode private constructor(
    val version: Int,
    private val ecl: Ecc,
    dataCodewords: ByteArray,
) {
    val size: Int = version * 4 + 17
    private val modules: Array<BooleanArray> = Array(size) { BooleanArray(size) }
    private val isFunction: Array<BooleanArray> = Array(size) { BooleanArray(size) }

    init {
        drawFunctionPatterns()
        val allCodewords = addEccAndInterleave(dataCodewords)
        drawCodewords(allCodewords)

        // Pick the mask with the lowest penalty.
        var minPenalty = Int.MAX_VALUE
        var bestMask = 0
        for (m in 0 until 8) {
            applyMask(m)
            drawFormatBits(m)
            val penalty = getPenaltyScore()
            if (penalty < minPenalty) {
                bestMask = m
                minPenalty = penalty
            }
            applyMask(m) // XOR is its own inverse -> undo
        }
        applyMask(bestMask)
        drawFormatBits(bestMask)
    }

    fun getModule(x: Int, y: Int): Boolean =
        x in 0 until size && y in 0 until size && modules[y][x]

    // -------------------------------------------------------- function patterns

    private fun drawFunctionPatterns() {
        for (i in 0 until size) {
            setFunctionModule(6, i, i % 2 == 0)
            setFunctionModule(i, 6, i % 2 == 0)
        }
        drawFinderPattern(3, 3)
        drawFinderPattern(size - 4, 3)
        drawFinderPattern(3, size - 4)

        val pos = alignmentPatternPositions()
        val n = pos.size
        for (i in 0 until n) {
            for (j in 0 until n) {
                if (!((i == 0 && j == 0) || (i == 0 && j == n - 1) || (i == n - 1 && j == 0))) {
                    drawAlignmentPattern(pos[i], pos[j])
                }
            }
        }
        drawFormatBits(0) // reserve format area with dummy data
        drawVersion()
    }

    private fun drawFormatBits(mask: Int) {
        val data = ecl.formatBits shl 3 or mask
        var rem = data
        for (i in 0 until 10) rem = rem shl 1 xor ((rem ushr 9) * 0x537)
        val bits = (data shl 10 or rem) xor 0x5412
        for (i in 0..5) setFunctionModule(8, i, getBit(bits, i))
        setFunctionModule(8, 7, getBit(bits, 6))
        setFunctionModule(8, 8, getBit(bits, 7))
        setFunctionModule(7, 8, getBit(bits, 8))
        for (i in 9 until 15) setFunctionModule(14 - i, 8, getBit(bits, i))
        for (i in 0 until 8) setFunctionModule(size - 1 - i, 8, getBit(bits, i))
        for (i in 8 until 15) setFunctionModule(8, size - 15 + i, getBit(bits, i))
        setFunctionModule(8, size - 8, true)
    }

    private fun drawVersion() {
        if (version < 7) return
        var rem = version
        for (i in 0 until 12) rem = rem shl 1 xor ((rem ushr 11) * 0x1F25)
        val bits = version shl 12 or rem
        for (i in 0 until 18) {
            val bit = getBit(bits, i)
            val a = size - 11 + i % 3
            val b = i / 3
            setFunctionModule(a, b, bit)
            setFunctionModule(b, a, bit)
        }
    }

    private fun drawFinderPattern(x: Int, y: Int) {
        for (dy in -4..4) {
            for (dx in -4..4) {
                val dist = max(abs(dx), abs(dy))
                val xx = x + dx
                val yy = y + dy
                if (xx in 0 until size && yy in 0 until size) {
                    setFunctionModule(xx, yy, dist != 2 && dist != 4)
                }
            }
        }
    }

    private fun drawAlignmentPattern(x: Int, y: Int) {
        for (dy in -2..2) {
            for (dx in -2..2) {
                setFunctionModule(x + dx, y + dy, max(abs(dx), abs(dy)) != 1)
            }
        }
    }

    private fun setFunctionModule(x: Int, y: Int, isDark: Boolean) {
        modules[y][x] = isDark
        isFunction[y][x] = true
    }

    // ------------------------------------------------------------ data + ECC

    private fun addEccAndInterleave(data: ByteArray): ByteArray {
        val numBlocks = NUM_ERROR_CORRECTION_BLOCKS[ecl.ordinal][version]
        val blockEccLen = ECC_CODEWORDS_PER_BLOCK[ecl.ordinal][version]
        val rawCodewords = numRawDataModules(version) / 8
        val numShortBlocks = numBlocks - rawCodewords % numBlocks
        val shortBlockLen = rawCodewords / numBlocks

        val blocks = arrayOfNulls<ByteArray>(numBlocks)
        val rsDiv = reedSolomonComputeDivisor(blockEccLen)
        var k = 0
        for (i in 0 until numBlocks) {
            val datLen = shortBlockLen - blockEccLen + (if (i < numShortBlocks) 0 else 1)
            val dat = data.copyOfRange(k, k + datLen)
            k += datLen
            val block = dat.copyOf(shortBlockLen + 1)
            val ecc = reedSolomonComputeRemainder(dat, rsDiv)
            System.arraycopy(ecc, 0, block, block.size - blockEccLen, blockEccLen)
            blocks[i] = block
        }

        val result = ByteArray(rawCodewords)
        var idx = 0
        for (i in 0 until blocks[0]!!.size) {
            for (j in blocks.indices) {
                // Skip the padding byte of the short blocks.
                if (i != shortBlockLen - blockEccLen || j >= numShortBlocks) {
                    result[idx] = blocks[j]!![i]
                    idx++
                }
            }
        }
        return result
    }

    private fun drawCodewords(data: ByteArray) {
        var i = 0
        var right = size - 1
        while (right >= 1) {
            if (right == 6) right = 5
            for (vert in 0 until size) {
                for (j in 0 until 2) {
                    val x = right - j
                    val upward = (right + 1) and 2 == 0
                    val y = if (upward) size - 1 - vert else vert
                    if (!isFunction[y][x] && i < data.size * 8) {
                        modules[y][x] = getBit(data[i ushr 3].toInt(), 7 - (i and 7))
                        i++
                    }
                }
            }
            right -= 2
        }
    }

    private fun applyMask(mask: Int) {
        for (y in 0 until size) {
            for (x in 0 until size) {
                val invert = when (mask) {
                    0 -> (x + y) % 2 == 0
                    1 -> y % 2 == 0
                    2 -> x % 3 == 0
                    3 -> (x + y) % 3 == 0
                    4 -> (x / 3 + y / 2) % 2 == 0
                    5 -> x * y % 2 + x * y % 3 == 0
                    6 -> (x * y % 2 + x * y % 3) % 2 == 0
                    7 -> ((x + y) % 2 + x * y % 3) % 2 == 0
                    else -> false
                }
                if (!isFunction[y][x] && invert) modules[y][x] = !modules[y][x]
            }
        }
    }

    // ---------------------------------------------------------- penalty score

    private fun getPenaltyScore(): Int {
        var result = 0
        // Rows.
        for (y in 0 until size) {
            var runColor = false
            var runX = 0
            val runHistory = IntArray(7)
            for (x in 0 until size) {
                if (modules[y][x] == runColor) {
                    runX++
                    if (runX == 5) result += PENALTY_N1 else if (runX > 5) result++
                } else {
                    finderPenaltyAddHistory(runX, runHistory)
                    if (!runColor) result += finderPenaltyCountPatterns(runHistory) * PENALTY_N3
                    runColor = modules[y][x]
                    runX = 1
                }
            }
            result += finderPenaltyTerminateAndCount(runColor, runX, runHistory) * PENALTY_N3
        }
        // Columns.
        for (x in 0 until size) {
            var runColor = false
            var runY = 0
            val runHistory = IntArray(7)
            for (y in 0 until size) {
                if (modules[y][x] == runColor) {
                    runY++
                    if (runY == 5) result += PENALTY_N1 else if (runY > 5) result++
                } else {
                    finderPenaltyAddHistory(runY, runHistory)
                    if (!runColor) result += finderPenaltyCountPatterns(runHistory) * PENALTY_N3
                    runColor = modules[y][x]
                    runY = 1
                }
            }
            result += finderPenaltyTerminateAndCount(runColor, runY, runHistory) * PENALTY_N3
        }
        // 2x2 blocks of one color.
        for (y in 0 until size - 1) {
            for (x in 0 until size - 1) {
                val c = modules[y][x]
                if (c == modules[y][x + 1] && c == modules[y + 1][x] && c == modules[y + 1][x + 1]) {
                    result += PENALTY_N2
                }
            }
        }
        // Balance of dark vs light.
        var dark = 0
        for (row in modules) for (c in row) if (c) dark++
        val total = size * size
        val k = (abs(dark * 20 - total * 10) + total - 1) / total - 1
        result += k * PENALTY_N4
        return result
    }

    private fun finderPenaltyCountPatterns(runHistory: IntArray): Int {
        val n = runHistory[1]
        val core = n > 0 && runHistory[2] == n && runHistory[3] == n * 3 &&
            runHistory[4] == n && runHistory[5] == n
        return (if (core && runHistory[0] >= n * 4 && runHistory[6] >= n) 1 else 0) +
            (if (core && runHistory[6] >= n * 4 && runHistory[0] >= n) 1 else 0)
    }

    private fun finderPenaltyTerminateAndCount(
        currentRunColor: Boolean,
        currentRunLength: Int,
        runHistory: IntArray,
    ): Int {
        var len = currentRunLength
        if (currentRunColor) {
            finderPenaltyAddHistory(len, runHistory)
            len = 0
        }
        len += size
        finderPenaltyAddHistory(len, runHistory)
        return finderPenaltyCountPatterns(runHistory)
    }

    private fun finderPenaltyAddHistory(currentRunLength: Int, runHistory: IntArray) {
        var len = currentRunLength
        if (runHistory[0] == 0) len += size
        System.arraycopy(runHistory, 0, runHistory, 1, runHistory.size - 1)
        runHistory[0] = len
    }

    private fun alignmentPatternPositions(): IntArray {
        if (version == 1) return IntArray(0)
        val numAlign = version / 7 + 2
        val step = if (version == 32) 26 else (version * 4 + numAlign * 2 + 1) / (numAlign * 2 - 2) * 2
        val result = IntArray(numAlign)
        result[0] = 6
        var i = result.size - 1
        var p = size - 7
        while (i >= 1) {
            result[i] = p
            i--
            p -= step
        }
        return result
    }

    companion object {
        private const val PENALTY_N1 = 3
        private const val PENALTY_N2 = 3
        private const val PENALTY_N3 = 40
        private const val PENALTY_N4 = 10

        /** Encode a string in UTF-8 byte mode at the given ECC level. */
        fun encodeText(text: String, ecl: Ecc = Ecc.MEDIUM): QrCode =
            encodeBytes(text.toByteArray(Charsets.UTF_8), ecl)

        fun encodeBytes(data: ByteArray, ecl: Ecc): QrCode {
            // Smallest version that fits the byte segment at this ECC level.
            var version = -1
            for (v in 1..40) {
                val capacityBits = numDataCodewords(v, ecl) * 8
                val usedBits = 4 + charCountBits(v) + data.size * 8
                if (usedBits <= capacityBits) {
                    version = v
                    break
                }
            }
            require(version != -1) { "Data too long for a QR code" }

            val capacityBits = numDataCodewords(version, ecl) * 8
            val bb = ArrayList<Boolean>(capacityBits)
            appendBits(bb, 4, 4) // byte-mode indicator 0b0100
            appendBits(bb, data.size, charCountBits(version))
            for (b in data) appendBits(bb, b.toInt() and 0xFF, 8)
            // Terminator + byte alignment.
            appendBits(bb, 0, min(4, capacityBits - bb.size))
            appendBits(bb, 0, (8 - bb.size % 8) % 8)
            // Pad bytes.
            var pad = 0xEC
            while (bb.size < capacityBits) {
                appendBits(bb, pad, 8)
                pad = pad xor (0xEC xor 0x11)
            }
            val dataCodewords = ByteArray(bb.size / 8)
            for (i in bb.indices) {
                if (bb[i]) {
                    dataCodewords[i ushr 3] =
                        (dataCodewords[i ushr 3].toInt() or (1 shl (7 - (i and 7)))).toByte()
                }
            }
            return QrCode(version, ecl, dataCodewords)
        }

        // Byte-mode character-count bits: 8 for versions 1-9, else 16.
        private fun charCountBits(version: Int): Int = if (version <= 9) 8 else 16

        private fun appendBits(bb: MutableList<Boolean>, value: Int, len: Int) {
            for (i in len - 1 downTo 0) bb.add((value ushr i) and 1 != 0)
        }

        private fun getBit(x: Int, i: Int): Boolean = (x ushr i) and 1 != 0

        private fun numDataCodewords(version: Int, ecl: Ecc): Int =
            numRawDataModules(version) / 8 -
                ECC_CODEWORDS_PER_BLOCK[ecl.ordinal][version] *
                NUM_ERROR_CORRECTION_BLOCKS[ecl.ordinal][version]

        private fun numRawDataModules(ver: Int): Int {
            var result = (16 * ver + 128) * ver + 64
            if (ver >= 2) {
                val numAlign = ver / 7 + 2
                result -= (25 * numAlign - 10) * numAlign - 55
                if (ver >= 7) result -= 36
            }
            return result
        }

        // ----------------------------------------------------- Reed-Solomon (GF(256))

        private fun reedSolomonComputeDivisor(degree: Int): ByteArray {
            val result = ByteArray(degree)
            result[degree - 1] = 1
            var root = 1
            for (i in 0 until degree) {
                for (j in result.indices) {
                    result[j] = reedSolomonMultiply(result[j].toInt() and 0xFF, root).toByte()
                    if (j + 1 < result.size) result[j] = (result[j].toInt() xor result[j + 1].toInt()).toByte()
                }
                root = reedSolomonMultiply(root, 0x02)
            }
            return result
        }

        private fun reedSolomonComputeRemainder(data: ByteArray, divisor: ByteArray): ByteArray {
            val result = ByteArray(divisor.size)
            for (b in data) {
                val factor = (b.toInt() xor result[0].toInt()) and 0xFF
                System.arraycopy(result, 1, result, 0, result.size - 1)
                result[result.size - 1] = 0
                for (i in result.indices) {
                    result[i] = (result[i].toInt() xor reedSolomonMultiply(divisor[i].toInt() and 0xFF, factor)).toByte()
                }
            }
            return result
        }

        private fun reedSolomonMultiply(x: Int, y: Int): Int {
            var z = 0
            for (i in 7 downTo 0) {
                z = z shl 1 xor ((z ushr 7) * 0x11D)
                z = z xor (((y ushr i) and 1) * x)
            }
            return z and 0xFF
        }

        // Tables indexed by [ecl.ordinal (L,M,Q,H)][version 1..40]; index 0 is padding.
        private val ECC_CODEWORDS_PER_BLOCK = arrayOf(
            intArrayOf(-1, 7, 10, 15, 20, 26, 18, 20, 24, 30, 18, 20, 24, 26, 30, 22, 24, 28, 30, 28, 28, 28, 28, 30, 30, 26, 28, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30),
            intArrayOf(-1, 10, 16, 26, 18, 24, 16, 18, 22, 22, 26, 30, 22, 22, 24, 24, 28, 28, 26, 26, 26, 26, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28),
            intArrayOf(-1, 13, 22, 18, 26, 18, 24, 18, 22, 20, 24, 28, 26, 24, 20, 30, 24, 28, 28, 26, 30, 28, 30, 30, 30, 30, 28, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30),
            intArrayOf(-1, 17, 28, 22, 16, 22, 28, 26, 26, 24, 28, 24, 28, 22, 24, 24, 30, 28, 28, 26, 28, 30, 24, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30),
        )
        private val NUM_ERROR_CORRECTION_BLOCKS = arrayOf(
            intArrayOf(-1, 1, 1, 1, 1, 1, 2, 2, 2, 2, 4, 4, 4, 4, 4, 6, 6, 6, 6, 7, 8, 8, 9, 9, 10, 12, 12, 12, 13, 14, 15, 16, 17, 18, 19, 19, 20, 21, 22, 24, 25),
            intArrayOf(-1, 1, 1, 1, 2, 2, 4, 4, 4, 5, 5, 5, 8, 9, 9, 10, 10, 11, 13, 14, 16, 17, 17, 18, 20, 21, 23, 25, 26, 28, 29, 31, 33, 35, 37, 38, 40, 43, 45, 47, 49),
            intArrayOf(-1, 1, 1, 2, 2, 4, 4, 6, 6, 8, 8, 8, 10, 12, 16, 12, 17, 16, 18, 21, 20, 23, 23, 25, 27, 29, 34, 34, 35, 38, 40, 43, 45, 48, 51, 53, 56, 59, 62, 65, 68),
            intArrayOf(-1, 1, 1, 2, 4, 4, 4, 5, 6, 8, 8, 11, 11, 16, 16, 18, 16, 19, 21, 25, 25, 25, 34, 30, 32, 35, 37, 40, 42, 45, 48, 51, 54, 57, 60, 63, 66, 70, 74, 77, 81),
        )
    }
}

/** Draws a QR code for [data] (default Material colors) on a square Canvas. */
@Composable
fun QrImage(data: String, modifier: Modifier = Modifier) {
    val qr = remember(data) { QrCode.encodeText(data, Ecc.MEDIUM) }
    Canvas(modifier) {
        val quiet = 2
        val total = qr.size + quiet * 2
        val cell = min(size.width, size.height) / total
        drawRect(Color.White, size = Size(cell * total, cell * total))
        for (y in 0 until qr.size) {
            for (x in 0 until qr.size) {
                if (qr.getModule(x, y)) {
                    drawRect(
                        color = Color.Black,
                        topLeft = Offset((x + quiet) * cell, (y + quiet) * cell),
                        size = Size(cell, cell),
                    )
                }
            }
        }
    }
}
