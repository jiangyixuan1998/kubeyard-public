<template>
  <router-view />
</template>

<script setup lang="ts">
</script>

<style>
/* ========== KubeYard interface themes ========== */
:root,
html[data-theme='classic'] {
  /* Element Plus 主色：玫瑰红（较品牌红降饱和提亮，白底上柔和不刺眼） */
  --el-color-primary: #C84158;
  --el-color-primary-light-3: #D97A8A;
  --el-color-primary-light-5: #E4A0AB;
  --el-color-primary-light-7: #EFC6CD;
  --el-color-primary-light-8: #F4D8DD;
  --el-color-primary-light-9: #FAEBED;
  --el-color-primary-dark-2: #A03446;
  /* 全局圆角：按钮/输入框/卡片更柔和 */
  --el-border-radius-base: 8px;
  --el-border-radius-small: 6px;
  --el-card-border-radius: 12px;
  /* 侧栏（暖白底 + 玫瑰点缀） */
  --sidebar-bg: #faf8f9;
  --sidebar-text: #6f6670;
  --sidebar-active: #BC3552;
  --sidebar-active-bg: #F9E9ED;
  --sidebar-hover-bg: #f2edf0;
  --sidebar-brand-color: #3d3540;
  --sidebar-brand-bg: transparent;
  --sidebar-group: #b5adb7;
  --sidebar-border: #f0ebee;
  --header-border: transparent;
  --page-bg: #f7f5f6;
  /* 顶栏默认值（白底深字）：其余浅色主题未覆盖时沿用 */
  --header-bg: #ffffff;
  --header-title: #1f2d3d;
  --header-text: #606266;
  --header-avatar-bg: var(--el-color-primary);
  --header-btn-hover-color: var(--el-color-primary);
  --header-btn-hover-bg: var(--el-color-primary-light-9);
}
html[data-theme='brand'] {
  /* 品牌红：炭黑侧栏 + 红色强调 */
  --el-color-primary: #BE0915;
  --el-color-primary-light-3: #D1535B;
  --el-color-primary-light-5: #DF848A;
  --el-color-primary-light-7: #ECB5B9;
  --el-color-primary-light-8: #F2CED0;
  --el-color-primary-light-9: #F9E6E8;
  --el-color-primary-dark-2: #980711;
  /* 侧栏：炭黑底 + 激活项红色蒙层 */
  --sidebar-bg: #333333;
  --sidebar-text: #999999;
  --sidebar-active: #ffffff;
  --sidebar-active-bg: rgba(188, 0, 12, .2);
  --sidebar-hover-bg: rgba(230, 230, 230, .2);
  --sidebar-brand-color: #ffffff;
  --sidebar-brand-bg: #860e00;
  --sidebar-group: rgba(255, 255, 255, .4);
  --sidebar-border: transparent;
  /* 顶栏：深色导航条，白字标题 */
  --header-bg: #333333;
  --header-border: transparent;
  --header-title: #ffffff;
  --header-text: #cccccc;
  --header-avatar-bg: #980005;
  --header-btn-hover-color: #ffffff;
  --header-btn-hover-bg: rgba(230, 230, 230, .2);
  --page-bg: #f5f5f5;
}
html[data-theme='slate'] {
  /* 雅致黑：炭黑侧栏 + 亮红点缀 */
  --sidebar-bg: #1d2129;
  --sidebar-text: #a9aeb8;
  --sidebar-active: #F53F5F;
  --sidebar-active-bg: rgba(245, 63, 95, .16);
  --sidebar-hover-bg: rgba(255, 255, 255, .06);
  --sidebar-brand-color: #ffffff;
  --sidebar-brand-bg: rgba(255, 255, 255, .04);
  --sidebar-group: #6b7280;
  --sidebar-border: transparent;
  --header-border: #e8e9ec;
  --page-bg: #f7f7f8;
}
html[data-theme='azure'] {
  /* 静谧蓝：浅蓝灰侧栏 + 蓝色主色 */
  --el-color-primary: #4080FF;
  --el-color-primary-light-3: #79A6FF;
  --el-color-primary-light-5: #A0C0FF;
  --el-color-primary-light-7: #C6D9FF;
  --el-color-primary-light-8: #D9E5FF;
  --el-color-primary-light-9: #ECF2FF;
  --el-color-primary-dark-2: #3366CC;
  --sidebar-bg: #f7f9fc;
  --sidebar-text: #5d6b80;
  --sidebar-active: #2E6BE6;
  --sidebar-active-bg: #E8F0FE;
  --sidebar-hover-bg: #eef3fa;
  --sidebar-brand-color: #24324a;
  --sidebar-brand-bg: transparent;
  --sidebar-group: #a8b3c4;
  --sidebar-border: #e8edf4;
  --header-border: transparent;
  --page-bg: #f6f8fb;
}
html[data-theme='green'] {
  /* 青竹绿：浅绿灰侧栏 + 青绿主色 */
  --el-color-primary: #10A37F;
  --el-color-primary-light-3: #58BEA5;
  --el-color-primary-light-5: #88D1BF;
  --el-color-primary-light-7: #B7E2D9;
  --el-color-primary-light-8: #CFEBE6;
  --el-color-primary-light-9: #E7F3F4;
  --el-color-primary-dark-2: #0D8266;
  --sidebar-bg: #f6faf8;
  --sidebar-text: #5c6f66;
  --sidebar-active: #0C8F72;
  --sidebar-active-bg: #E3F4EE;
  --sidebar-hover-bg: #edf5f1;
  --sidebar-brand-color: #2f4038;
  --sidebar-brand-bg: transparent;
  --sidebar-group: #a4b5ac;
  --sidebar-border: #e6efe9;
  --header-border: transparent;
  --page-bg: #f5f8f6;
}
html, body, #app {
  height: 100%;
  margin: 0;
  padding: 0;
  background: var(--page-bg, #f7f7f8);
}
body { -webkit-font-smoothing: antialiased; }
* { box-sizing: border-box; }
</style>
