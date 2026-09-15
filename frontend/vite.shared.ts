// Keep the shared language catalog out of UI/vendor chunks without increasing
// the production bundle budgets enforced by check-dist.
const languageChunks = [
  { name: "i18n-common", test: /[\\/]i18n[\\/](?:index\.ts|(?:en|zh-CN)[\\/]common\.json)$/, priority: 100 },
  { name: "i18n-admin", test: /[\\/]i18n[\\/](?:admin\.ts|(?:en|zh-CN)[\\/]admin\.json)$/, priority: 100 },
  { name: "i18n-usage", test: /[\\/]i18n[\\/](?:usage\.ts|(?:en|zh-CN)[\\/]usage\.json)$/, priority: 100 }
];

export const adminCodeSplitting = {
  minSize: 20 * 1024,
  includeDependenciesRecursively: true,
  groups: [
    ...languageChunks,
    {
      name: "zrender-vendor",
      test: /node_modules[\\/]zrender[\\/]/,
      priority: 80
    },
    {
      name: "echarts-vendor",
      test: /node_modules[\\/]echarts[\\/]/,
      priority: 75
    }
  ]
};

export const portalCodeSplitting = {
  minSize: 20 * 1024,
  includeDependenciesRecursively: true,
  groups: [
    ...languageChunks,
    {
      name: "react-vendor",
      test: /node_modules[\\/]((react)|(react-dom)|(scheduler))[\\/]/,
      priority: 70
    },
    {
      name: "antd-icons-vendor",
      test: /node_modules[\\/]@ant-design[\\/]icons[\\/]/,
      priority: 65
    },
    {
      name: "antd-vendor",
      test: /node_modules[\\/]antd[\\/]/,
      priority: 60
    },
    {
      name: "data-vendor",
      test: /node_modules[\\/]((@tanstack)|(react-router)|(react-router-dom))[\\/]/,
      priority: 50
    }
  ]
};

export const usageCodeSplitting = {
  minSize: 20 * 1024,
  includeDependenciesRecursively: true,
  groups: [
    ...languageChunks,
    {
      name: "zrender-vendor",
      test: /node_modules[\\/]zrender[\\/]/,
      priority: 80
    },
    {
      name: "echarts-vendor",
      test: /node_modules[\\/]echarts[\\/]/,
      priority: 75
    },
    {
      name: "react-vendor",
      test: /node_modules[\\/]((react)|(react-dom)|(scheduler))[\\/]/,
      priority: 70
    },
    {
      name: "antd-icons-vendor",
      test: /node_modules[\\/]@ant-design[\\/]icons[\\/]/,
      priority: 65
    },
    {
      name: "forms-vendor",
      test: /node_modules[\\/]((react-hook-form)|(@hookform)|(zod))[\\/]/,
      priority: 55
    },
    {
      name: "data-vendor",
      test: /node_modules[\\/]((@tanstack)|(react-router)|(react-router-dom))[\\/]/,
      priority: 50
    }
  ]
};
