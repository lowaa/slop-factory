import { describe, expect, it } from "vitest";
import * as ui from "./index";
import ButtonSubpath from "./button/button";
import ModelessDialogSubpath from "./modeless_dialog/modeless_dialog";
import LoadingSpinnerSubpath from "./loading_spinner/loading_spinner";
import TooltipSubpath from "./tooltip/tooltip";
import MarkdownViewSubpath from "./markdown_view/markdown_view";
import DialogDockSubpath from "./modeless_dialog/dialog_dock";

// Hosts import through the package entry, so a missing or stale export
// would break them while every component-level test stays green.
describe("@slop-factory/ui public exports", () => {
  it("exports every component from the package entry", () => {
    expect(ui.Button).toBeTypeOf("function");
    expect(ui.Dropdown).toBeTypeOf("function");
    expect(ui.FilterableDropdown).toBeTypeOf("function");
    expect(ui.ModelessDialog).toBeTypeOf("function");
    expect(ui.DialogDock).toBeTypeOf("function");
    expect(ui.Tooltip).toBeTypeOf("function");
    expect(ui.LoadingSpinner).toBeTypeOf("function");
    expect(ui.MarkdownView).toBeTypeOf("object");
  });

  it("maps each package.json subpath to the component it names", async () => {
    const pkg = (await import("../package.json")).default as { exports: Record<string, string> };

    expect(pkg.exports["./button"]).toBe("./src/button/button.tsx");
    expect(ui.Button).toBe(ButtonSubpath);
    expect(pkg.exports["./modeless_dialog"]).toBe("./src/modeless_dialog/modeless_dialog.tsx");
    expect(ui.ModelessDialog).toBe(ModelessDialogSubpath);
    expect(pkg.exports["./dialog_dock"]).toBe("./src/modeless_dialog/dialog_dock.tsx");
    expect(ui.DialogDock).toBe(DialogDockSubpath);
    expect(pkg.exports["./loading_spinner"]).toBe("./src/loading_spinner/loading_spinner.tsx");
    expect(ui.LoadingSpinner).toBe(LoadingSpinnerSubpath);
    expect(pkg.exports["./tooltip"]).toBe("./src/tooltip/tooltip.tsx");
    expect(ui.Tooltip).toBe(TooltipSubpath);
    expect(pkg.exports["./markdown_view"]).toBe("./src/markdown_view/markdown_view.tsx");
    expect(ui.MarkdownView).toBe(MarkdownViewSubpath);
  });
});
