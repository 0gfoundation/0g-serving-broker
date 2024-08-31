import { AliasToken } from "antd/es/theme/interface";
import { ThemeConfig } from "antd/lib/config-provider/context";

// =begin:craco
export const token: Partial<AliasToken> = {
  borderRadius: 2,
  colorBorder: "#d9d9d9",
  colorError: "#ff4d4f",
  colorFillQuaternary: "rgba(0, 0, 0, 0.02)",
  colorPrimary: "#1188ed",
  controlHeight: 30,
  boxShadowTertiary:
    "0 1px 2px 0 rgba(0, 0, 0, .03),0 1px 6px -1px rgba(0, 0, 0, .02),0 2px 4px 0 rgba(0, 0, 0, .02)",
};
// =end:craco

const AntThemeConfig: ThemeConfig = {
  token: {
    ...token,
  },
  components: {
    Table: {
      cellPaddingBlock: 8,
      cellPaddingInline: 5,
    },
  },
};

export default AntThemeConfig;
