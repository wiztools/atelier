import AppKit
import Foundation

struct IconColors {
    static let canvas = NSColor(calibratedRed: 21.0 / 255.0, green: 23.0 / 255.0, blue: 25.0 / 255.0, alpha: 1.0)
    static let tile = NSColor(calibratedRed: 29.0 / 255.0, green: 32.0 / 255.0, blue: 34.0 / 255.0, alpha: 1.0)
    static let border = NSColor(calibratedRed: 184.0 / 255.0, green: 144.0 / 255.0, blue: 92.0 / 255.0, alpha: 1.0)
    static let letter = NSColor(calibratedRed: 255.0 / 255.0, green: 207.0 / 255.0, blue: 123.0 / 255.0, alpha: 1.0)
}

func renderIcon(size: Int) -> NSImage {
    let canvas = NSRect(x: 0, y: 0, width: size, height: size)
    let image = NSImage(size: canvas.size)
    image.lockFocus()
    defer { image.unlockFocus() }

    IconColors.canvas.setFill()
    canvas.fill()

    let scale = CGFloat(size) / 1024.0
    let inset = 92.0 * scale
    let lineWidth = 9.0 * scale
    let tileRect = canvas.insetBy(dx: inset, dy: inset)
    let tilePath = NSBezierPath(roundedRect: tileRect, xRadius: 39.0 * scale, yRadius: 39.0 * scale)

    IconColors.tile.setFill()
    tilePath.fill()
    IconColors.border.setStroke()
    tilePath.lineWidth = lineWidth
    tilePath.stroke()

    let fontSize = 690.0 * scale
    let font = NSFont(name: "Georgia", size: fontSize) ?? NSFont.systemFont(ofSize: fontSize, weight: .regular)
    let paragraph = NSMutableParagraphStyle()
    paragraph.alignment = .center
    let attrs: [NSAttributedString.Key: Any] = [
        .font: font,
        .foregroundColor: IconColors.letter,
        .paragraphStyle: paragraph,
    ]
    let glyph = NSAttributedString(string: "A", attributes: attrs)
    let glyphSize = glyph.size()
    let glyphRect = NSRect(
        x: (CGFloat(size) - glyphSize.width) / 2.0,
        y: (CGFloat(size) - glyphSize.height) / 2.0 - 28.0 * scale,
        width: glyphSize.width,
        height: glyphSize.height
    )
    glyph.draw(in: glyphRect)

    return image
}

func pngData(from image: NSImage) throws -> Data {
    guard let tiff = image.tiffRepresentation,
          let bitmap = NSBitmapImageRep(data: tiff),
          let data = bitmap.representation(using: .png, properties: [:]) else {
        throw NSError(domain: "AtelierIcon", code: 1, userInfo: [NSLocalizedDescriptionKey: "Could not encode PNG"])
    }
    return data
}

func icoData(from png: Data) -> Data {
    var data = Data()
    func appendUInt16(_ value: UInt16) {
        data.append(UInt8(value & 0xff))
        data.append(UInt8((value >> 8) & 0xff))
    }
    func appendUInt32(_ value: UInt32) {
        data.append(UInt8(value & 0xff))
        data.append(UInt8((value >> 8) & 0xff))
        data.append(UInt8((value >> 16) & 0xff))
        data.append(UInt8((value >> 24) & 0xff))
    }

    appendUInt16(0)
    appendUInt16(1)
    appendUInt16(1)
    data.append(0)
    data.append(0)
    data.append(0)
    data.append(0)
    appendUInt16(1)
    appendUInt16(32)
    appendUInt32(UInt32(png.count))
    appendUInt32(22)
    data.append(png)
    return data
}

let root = URL(fileURLWithPath: FileManager.default.currentDirectoryPath)
let png1024 = try pngData(from: renderIcon(size: 1024))
try png1024.write(to: root.appendingPathComponent("build/appicon.png"))

let png256 = try pngData(from: renderIcon(size: 256))
let ico = icoData(from: png256)
try ico.write(to: root.appendingPathComponent("build/windows/icon.ico"))
